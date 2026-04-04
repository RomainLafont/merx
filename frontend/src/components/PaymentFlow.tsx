import { useState, useEffect, useCallback } from "react";
import {
  useAccount,
  useReadContract,
  useReadContracts,
  useSendTransaction,
  useWaitForTransactionReceipt,
  useWriteContract,
  useSignTypedData,
  useSwitchChain,
  useChainId,
  usePublicClient,
} from "wagmi";
import { erc20Abi, type Hex } from "viem";

import type { QuoteResponse } from "@/types/uniswap";
import type { ChainInfo, TokenEntry } from "@/types/chain";
import type { Product } from "@/lib/products";
import { getQuote, buildSwap, getPayTx, submitPay } from "@/lib/api";
import { formatUSDC } from "@/lib/format";
import { ChainSelector } from "./ChainSelector";
import { TokenSelector } from "./TokenSelector";

const PERMIT2 = "0x000000000022D473030F116dDEE9F6B43aC78BA3" as Hex;
const MAX_UINT256 = 2n ** 256n - 1n;

type Step = "idle" | "select-token" | "quote" | "approving-permit2" | "signing-permit" | "swapping" | "transferring" | "approving-cctp" | "burning" | "confirming" | "done" | "error";

interface Props {
  chains: ChainInfo[];
  product: Product;
  onPaid: () => void;
}

export function PaymentFlow({ chains, product, onPaid }: Props) {
  const { address } = useAccount();
  const currentChainId = useChainId();
  const { switchChain } = useSwitchChain();
  const publicClient = usePublicClient();
  const [step, setStep] = useState<Step>("idle");
  const [selectedChainId, setSelectedChainId] = useState(0);
  const [selectedSymbol, setSelectedSymbol] = useState("USDC");

  // Amount in base units (6 decimals) from human-readable price
  const amountBaseUnits = String(Math.round(parseFloat(product.price) * 1_000_000));
  const [quote, setQuote] = useState<QuoteResponse | null>(null);
  const [error, setError] = useState("");
  const [loadingQuote, setLoadingQuote] = useState(false);

  const chain = chains.find((c) => c.chainId === selectedChainId);
  const allTokens = chain?.tokens ?? [];
  const selectedToken: TokenEntry | undefined = chain?.tokens.find((t) => t.symbol === selectedSymbol);
  const usdcToken = chain?.tokens.find((t) => t.symbol === "USDC");
  const isDirectUSDC = selectedSymbol === "USDC";

  // Read balances for ALL tokens on the chain in one multicall
  const balanceContracts = allTokens.map((t) => ({
    address: t.address as Hex,
    abi: erc20Abi,
    functionName: "balanceOf" as const,
    args: address ? [address] : undefined,
    chainId: selectedChainId || undefined,
  }));

  const { data: allBalances, isLoading: balancesLoading } = useReadContracts({
    contracts: balanceContracts,
    query: { enabled: !!address && allTokens.length > 0 && selectedChainId > 0 },
  });

  // Build a map of symbol → balance
  const tokenBalances: Record<string, bigint> = {};
  if (allBalances) {
    allTokens.forEach((t, i) => {
      const result = allBalances[i];
      if (result?.status === "success" && result.result != null) {
        tokenBalances[t.symbol] = result.result as bigint;
      }
    });
  }

  const tokenBalance = selectedToken ? tokenBalances[selectedSymbol] : undefined;
  // Swap tokens: non-USDC tokens the user actually holds (balance > 0), on Uniswap-supported chains
  const swapTokens = chain?.uniswapSupported
    ? allTokens.filter((t) => t.symbol !== "USDC" && (tokenBalances[t.symbol] ?? 0n) > 0n)
    : [];

  // Check Permit2 allowance for selected token (needed before swap)
  const { data: permit2Allowance } = useReadContract({
    address: (!isDirectUSDC ? selectedToken?.address ?? undefined : undefined) as Hex | undefined,
    abi: erc20Abi,
    functionName: "allowance",
    args: address ? [address, PERMIT2] : undefined,
    chainId: selectedChainId || undefined,
    query: { enabled: !!address && !isDirectUSDC && !!selectedToken?.address && selectedChainId > 0 },
  });

  const needsPermit2Approval = !isDirectUSDC && (permit2Allowance === undefined || permit2Allowance === 0n);

  // Permit2 ERC-20 approval TX
  const {
    writeContract: writePermit2Approve,
    data: permit2ApproveHash,
    isPending: permit2ApprovePending,
  } = useWriteContract();

  const { isSuccess: permit2ApproveConfirmed, isError: permit2ApproveReverted } = useWaitForTransactionReceipt({
    hash: permit2ApproveHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 2_000,
  });

  // CCTP approve tx
  const { writeContract: writeCCTPApprove, data: cctpApproveHash, isPending: cctpApprovePending } = useWriteContract();
  const { isSuccess: cctpApproveConfirmed, isError: cctpApproveReverted } = useWaitForTransactionReceipt({
    hash: cctpApproveHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 2_000,
  });

  // CCTP burn tx (depositForBurnWithHook)
  const { sendTransaction: sendBurn, data: burnHash, isPending: burnPending } = useSendTransaction();
  const { isSuccess: burnConfirmed, isError: burnReverted } = useWaitForTransactionReceipt({
    hash: burnHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 2_000,
  });

  // Direct USDC transfer (legacy, for swap flow)
  const { data: transferHash, isPending: transferPending } = useWriteContract();
  const { isSuccess: transferConfirmed, isError: transferReverted } = useWaitForTransactionReceipt({
    hash: transferHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 2_000,
  });

  // Pay tx data from backend
  const [payTxData, setPayTxData] = useState<Awaited<ReturnType<typeof getPayTx>> | null>(null);
  const [invoiceId, setInvoiceId] = useState<string | null>(null);

  // Permit2 signature
  const { signTypedData, data: permitSignature, isPending: signPending, error: signError } = useSignTypedData();

  // Swap TX
  const { sendTransaction: sendSwap, data: swapHash, isPending: swapPending } = useSendTransaction();
  const { isSuccess: swapConfirmed, isError: swapReverted } = useWaitForTransactionReceipt({
    hash: swapHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 2_000,
  });

  // Error detection
  useEffect(() => {
    if (permit2ApproveReverted && step === "approving-permit2") { setError("Permit2 approval reverted on-chain."); setStep("error"); }
  }, [permit2ApproveReverted, step]);
  useEffect(() => {
    if (signError && step === "signing-permit") { setError(`Permit signature rejected: ${signError.message}`); setStep("error"); }
  }, [signError, step]);
  useEffect(() => {
    if (swapReverted && step === "swapping") { setError("Swap reverted on-chain. The quote may have expired."); setStep("error"); }
  }, [swapReverted, step]);
  useEffect(() => {
    if (transferReverted && step === "transferring") { setError("Transfer reverted on-chain."); setStep("error"); }
  }, [transferReverted, step]);
  useEffect(() => {
    if (cctpApproveReverted && step === "approving-cctp") { setError("USDC approval reverted."); setStep("error"); }
  }, [cctpApproveReverted, step]);
  useEffect(() => {
    if (burnReverted && step === "burning") { setError("CCTP burn reverted."); setStep("error"); }
  }, [burnReverted, step]);

  // After CCTP approve confirmed → wait for state propagation then send burn tx
  useEffect(() => {
    if (cctpApproveConfirmed && payTxData && step === "approving-cctp") {
      // Wait 3s for RPC state propagation (MetaMask simulates against latest state).
      const timer = setTimeout(() => sendBurnTx(payTxData), 3000);
      return () => clearTimeout(timer);
    }
  }, [cctpApproveConfirmed, payTxData, step]);

  // After burn confirmed → report to backend
  useEffect(() => {
    if (burnConfirmed && burnHash && step === "burning") {
      setStep("confirming");
      submitPay({
        txHash: burnHash,
        chainId: selectedChainId,
        amount: amountBaseUnits,
        owner: address!,
        description: `Purchase: ${product.title}`,
        productId: product.id,
      }).then((res) => {
        setInvoiceId(res.invoiceId);
        setStep("done");
        onPaid();
      }).catch((err) => {
        setError(err instanceof Error ? err.message : "Failed to confirm payment");
        setStep("error");
      });
    }
  }, [burnConfirmed, burnHash, step]);

  // After Permit2 ERC-20 approval confirmed, proceed to permit signature flow
  useEffect(() => {
    if (permit2ApproveConfirmed && step === "approving-permit2") {
      proceedToPermitSign();
    }
  }, [permit2ApproveConfirmed, step]);

  // After permit signed -> swap
  useEffect(() => {
    if (permitSignature && step === "signing-permit") { executeSwapWithPermit(permitSignature); }
  }, [permitSignature, step]);

  // After swap confirmed -> trigger CCTP payment
  useEffect(() => {
    if (swapConfirmed && step === "swapping") {
      doCCTPPayment();
    }
  }, [swapConfirmed, step]);

  // After USDC transfer confirmed (legacy flow) -> done
  useEffect(() => {
    if (transferConfirmed && transferHash) {
      setStep("done");
      onPaid();
    }
  }, [transferConfirmed, transferHash]);

  // --- Actions ---

  function handleChainSelect(chainId: number) {
    setSelectedChainId(chainId);
    setSelectedSymbol("USDC");
    setQuote(null);
    setError("");
    setStep("select-token");
    if (currentChainId !== chainId) {
      switchChain({ chainId });
    }
  }

  function handleChangeChain(chainId: number) {
    setSelectedSymbol("USDC");
    setQuote(null);
    setError("");
    handleChainSelect(chainId);
  }

  const fetchQuote = useCallback(async () => {
    if (!address || !selectedToken || isDirectUSDC) return;
    setLoadingQuote(true);
    setError("");
    try {
      const resp = await getQuote({ tokenIn: selectedToken.address, tokenInChainId: selectedChainId, amount: amountBaseUnits, swapper: address });
      setQuote(resp);
      setStep("quote");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to get quote");
    } finally {
      setLoadingQuote(false);
    }
  }, [address, selectedToken, isDirectUSDC, selectedChainId]);

  function sendBurnTx(ptx: Awaited<ReturnType<typeof getPayTx>>) {
    if (!address || !publicClient) return;
    setStep("burning");
    publicClient.getTransactionCount({ address, blockTag: "pending" }).then((nonce) => {
      sendBurn(
        {
          to: ptx.to as Hex,
          data: ptx.data as Hex,
          value: BigInt(ptx.value),
          gas: 400_000n, // enough for depositForBurnWithHook (~200k actual)
          nonce,
          chainId: ptx.chain_id,
        },
        { onError(err) { setError(`Burn TX failed: ${err.message}`); setStep("error"); } },
      );
    }).catch((err) => {
      setError(`Failed: ${err.message}`);
      setStep("error");
    });
  }

  async function doCCTPPayment() {
    if (!usdcToken || !address) return;
    setError("");
    try {
      const ptx = await getPayTx(selectedChainId, amountBaseUnits);
      setPayTxData(ptx);

      // Always approve before burn (allowance is consumed each time).
      setStep("approving-cctp");
      writeCCTPApprove({
        address: ptx.approval.token as Hex,
        abi: erc20Abi,
        functionName: "approve",
        args: [ptx.approval.spender as Hex, BigInt(ptx.approval.amount)],
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to prepare payment");
      setStep("error");
    }
  }

  async function handlePayWithSwap() {
    if (!address || !selectedToken || !address) { setError("Missing data."); setStep("error"); return; }

    // Step 0: if token not approved to Permit2, do ERC-20 approve first
    if (needsPermit2Approval) {
      setStep("approving-permit2");
      writePermit2Approve({
        address: selectedToken.address as Hex,
        abi: erc20Abi,
        functionName: "approve",
        args: [PERMIT2, MAX_UINT256],
      });
      return;
    }

    // Already approved to Permit2, go to permit signature
    await proceedToPermitSign();
  }

  async function proceedToPermitSign() {
    if (!address || !selectedToken) { setError("Missing data."); setStep("error"); return; }
    setStep("signing-permit");
    try {
      // Fetch a FRESH quote — the permit signature is tied to this exact quote.
      // The same quote MUST be used for buildSwap.
      const freshQuote = await getQuote({ tokenIn: selectedToken.address, tokenInChainId: selectedChainId, amount: amountBaseUnits, swapper: address });
      setQuote(freshQuote);
      if (!freshQuote.permitData) { pendingQuoteRef = freshQuote; await executeSwapWithPermit(undefined); return; }
      pendingQuoteRef = freshQuote;
      const pd = freshQuote.permitData;
      signTypedData(
        { domain: { name: pd.domain.name as string, chainId: Number(pd.domain.chainId), verifyingContract: pd.domain.verifyingContract as Hex }, types: pd.types as Record<string, Array<{ name: string; type: string }>>, primaryType: "PermitSingle", message: pd.values as Record<string, unknown> },
        { onError(err) { setError(`Permit failed: ${err.message}`); setStep("error"); } },
      );
    } catch (err) { setError(err instanceof Error ? err.message : "Failed to prepare swap"); setStep("error"); }
  }

  async function executeSwapWithPermit(signature?: string) {
    // Use the SAME quote that was signed — signature is tied to the permit data.
    const activeQuote = pendingQuoteRef ?? quote;
    if (!activeQuote) { setError("No quote available."); setStep("error"); return; }
    setStep("swapping");
    try {
      const swapResp = await buildSwap({
        quote: activeQuote.quote,
        ...(signature ? { signature } : {}),
        ...(activeQuote.permitData ? { permitData: activeQuote.permitData } : {}),
      });
      const tx = swapResp.swap;
      const apiGas = tx.gasLimit ? BigInt(tx.gasLimit) : 0n;
      const safeGas = apiGas < 300_000n ? 400_000n : apiGas;
      sendSwap(
        { to: tx.to as Hex, data: tx.data as Hex, value: tx.value ? BigInt(tx.value) : 0n, gas: safeGas, chainId: selectedChainId },
        { onError(err) { setError(`Swap TX failed: ${err.message}`); setStep("error"); } },
      );
    } catch (err) { setError(err instanceof Error ? err.message : "Swap failed"); setStep("error"); }
  }

  if (!address) {
    return <div className="text-center text-muted-foreground py-4">Connect your wallet to pay</div>;
  }

  // --- Render ---

  return (
    <div className="space-y-4">
      {/* Chain selector — always visible during selection steps */}
      {(step === "idle" || step === "select-token" || step === "quote") && (
        <div className="space-y-2">
          <label className="block text-sm text-muted-foreground">Network</label>
          <ChainSelector
            chains={chains}
            selected={chain}
            onSelect={(id) => chain?.chainId === id ? undefined : handleChangeChain(id)}
          />
        </div>
      )}

      {/* Token selection */}
      {step === "select-token" && chain && (
        <div className="space-y-4">

          {/* Token selector */}
          <div className="space-y-2">
            <label className="block text-sm text-muted-foreground">Pay with</label>
            {chain.uniswapSupported ? (
              <>
                <TokenSelector
                  options={[
                    ...(usdcToken ? [{ token: usdcToken, balance: tokenBalances["USDC"], isSwap: false }] : []),
                    ...swapTokens.map((t) => ({ token: t, balance: tokenBalances[t.symbol], isSwap: true })),
                  ]}
                  selected={selectedSymbol}
                  onSelect={setSelectedSymbol}
                  loading={balancesLoading}
                  formatBalance={formatTokenAmount}
                />
                {swapTokens.length === 0 && !balancesLoading && (
                  <p className="text-xs text-muted-foreground">No other tokens found in your wallet on {chain.name}.</p>
                )}
              </>
            ) : (
              <div className="rounded-md border border-border bg-background px-3 py-2.5 text-sm font-medium text-foreground">
                USDC
              </div>
            )}
          </div>

          {/* Action button */}
          {isDirectUSDC ? (
            <div className="space-y-2">
              <button
                onClick={doCCTPPayment}
                disabled={cctpApprovePending || burnPending}
                className="w-full rounded-md bg-[#2775CA] px-4 py-2.5 text-sm font-medium text-white hover:bg-[#2775CA]/90 disabled:opacity-50 transition-colors flex items-center justify-center gap-2"
              >
                <img src="/usdc.png" alt="" className="h-5 w-5" />
                {cctpApprovePending || burnPending ? "Confirm in wallet..." : `Pay ${product.price} USDC`}
              </button>
              <div className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
                <img src="/circle_logo.png" alt="" className="h-3.5 w-3.5" />
                <span>Powered by Circle</span>
              </div>
            </div>
          ) : (
            <button
              onClick={fetchQuote}
              disabled={loadingQuote}
              className="w-full rounded-md bg-[#ff007a] px-4 py-2 text-sm font-medium text-white hover:bg-[#ff007a]/90 disabled:opacity-50 transition-colors flex items-center justify-center gap-2"
            >
              <img src="/uniswap.png" alt="" className="h-4 w-4" />
              {loadingQuote ? "Getting quote..." : `Get ${selectedSymbol} Quote`}
            </button>
          )}
        </div>
      )}

      {/* Step 3: Quote display */}
      {step === "quote" && quote && selectedToken && chain && (
        <div className="space-y-4">
          <div className="rounded-lg border border-border bg-secondary/50 p-4 space-y-2">
            <div className="flex justify-between text-sm">
              <span className="text-muted-foreground">You pay</span>
              <span className="font-medium">{formatTokenAmount(quote.quote.input.amount, selectedToken.decimals)} {selectedSymbol}</span>
            </div>
            {tokenBalance !== undefined && (
              <div className="flex justify-between text-sm">
                <span className="text-muted-foreground">Your balance</span>
                <span className={`font-medium ${tokenBalance < BigInt(quote.quote.input.amount) ? "text-destructive" : "text-success"}`}>
                  {formatTokenAmount(tokenBalance.toString(), selectedToken.decimals)} {selectedSymbol}
                </span>
              </div>
            )}
            <div className="flex justify-between text-sm">
              <span className="text-muted-foreground">Merchant receives</span>
              <span className="font-medium">{formatUSDC(quote.quote.output.amount)} USDC</span>
            </div>
            <div className="flex justify-between text-sm">
              <span className="text-muted-foreground">Gas fee</span>
              <span>{quote.quote.gasFeeUSD ? `$${quote.quote.gasFeeUSD}` : "\u2014"}</span>
            </div>
            {quote.quote.priceImpact !== undefined && (
              <div className="flex justify-between text-sm">
                <span className="text-muted-foreground">Price impact</span>
                <span className={quote.quote.priceImpact > 1 ? "text-destructive" : ""}>{quote.quote.priceImpact.toFixed(2)}%</span>
              </div>
            )}
          </div>

          <div className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
            <img src="/uniswap.png" alt="" className="h-3.5 w-3.5" />
            <span>Powered by Uniswap</span>
          </div>

          <div className="flex gap-2">
            <button onClick={() => { setStep("select-token"); setQuote(null); }} className="flex-1 rounded-md border border-border px-4 py-2 text-sm text-muted-foreground hover:text-foreground transition-colors">Back</button>
            <button onClick={handlePayWithSwap} className="flex-1 rounded-md bg-[#ff007a] px-4 py-2 text-sm font-medium text-white hover:bg-[#ff007a]/90 transition-colors flex items-center justify-center gap-2">
              <img src="/uniswap.png" alt="" className="h-4 w-4" />
              Swap & Pay
            </button>
          </div>
        </div>
      )}

      {/* Progress states */}
      {step === "approving-permit2" && (
        <StatusMessage status="pending">
          {permit2ApprovePending
            ? "Approve token for Permit2 in your wallet..."
            : permit2ApproveHash
              ? "Waiting for approval confirmation..."
              : "Preparing approval..."}
        </StatusMessage>
      )}
      {step === "signing-permit" && (
        <StatusMessage status="pending">{signPending ? "Sign the permit in your wallet..." : "Preparing swap..."}</StatusMessage>
      )}
      {step === "swapping" && (
        <StatusMessage status="pending">{swapPending ? "Confirm swap in wallet..." : swapHash ? "Waiting for swap confirmation..." : "Building swap transaction..."}</StatusMessage>
      )}
      {step === "transferring" && (
        <StatusMessage status="pending">{transferPending ? "Confirm transfer in wallet..." : "Waiting for transfer confirmation..."}</StatusMessage>
      )}
      {step === "approving-cctp" && (
        <StatusMessage status="pending">{cctpApprovePending ? "Approve USDC in wallet (1/2)..." : "Waiting for approval confirmation..."}</StatusMessage>
      )}
      {step === "burning" && (
        <StatusMessage status="pending">{burnPending ? "Confirm bridge transaction in wallet (2/2)..." : "Waiting for bridge confirmation..."}</StatusMessage>
      )}
      {step === "confirming" && (
        <StatusMessage status="pending">Payment confirmed on-chain! Waiting for settlement on Arc...</StatusMessage>
      )}
      {step === "done" && (() => {
        const explorer = chain?.explorer;
        const swapUrl = explorer && swapHash ? `${explorer}/tx/${swapHash}` : undefined;
        const burnUrl = explorer && burnHash ? `${explorer}/tx/${burnHash}` : undefined;
        const transferUrl = explorer && transferHash ? `${explorer}/tx/${transferHash}` : undefined;
        const hasSwapFlow = !!(swapHash && (burnHash || transferHash));
        return (
          <div className="space-y-3">
            <StatusMessage status="success">
              <div>
                Payment confirmed!
                {hasSwapFlow ? (
                  <div className="flex gap-3 mt-1">
                    {swapUrl && <a href={swapUrl} target="_blank" rel="noopener noreferrer" className="underline hover:opacity-80">Swap tx</a>}
                    {(burnUrl || transferUrl) && <a href={(burnUrl || transferUrl)!} target="_blank" rel="noopener noreferrer" className="underline hover:opacity-80">Payment tx</a>}
                  </div>
                ) : (
                  <>{" "}{(burnUrl || transferUrl) ? (
                    <a href={(burnUrl || transferUrl)!} target="_blank" rel="noopener noreferrer" className="underline hover:opacity-80">View transaction</a>
                  ) : null}</>
                )}
              </div>
            </StatusMessage>
            {invoiceId && (
              <a
                href={`/api/ebooks/${invoiceId}`}
                download
                className="flex items-center justify-center gap-2 w-full rounded-md bg-success px-4 py-3 text-sm font-medium text-white hover:bg-success/90 transition-colors"
              >
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="currentColor" className="w-4 h-4">
                  <path d="M2.75 14A1.75 1.75 0 0 1 1 12.25v-2.5a.75.75 0 0 1 1.5 0v2.5c0 .138.112.25.25.25h10.5a.25.25 0 0 0 .25-.25v-2.5a.75.75 0 0 1 1.5 0v2.5A1.75 1.75 0 0 1 13.25 14H2.75Z" />
                  <path d="M7.25 7.689V2a.75.75 0 0 1 1.5 0v5.689l1.97-1.969a.749.749 0 1 1 1.06 1.06l-3.25 3.25a.749.749 0 0 1-1.06 0L4.22 6.78a.749.749 0 1 1 1.06-1.06l1.97 1.969Z" />
                </svg>
                Download your ebook
              </a>
            )}
          </div>
        );
      })()}
      {step === "error" && (
        <div className="space-y-2">
          <StatusMessage status="error">{error}</StatusMessage>
          <button onClick={() => { setStep("select-token"); setError(""); setQuote(null); }} className="w-full rounded-md border border-border px-4 py-2 text-sm text-muted-foreground hover:text-foreground transition-colors">Try Again</button>
        </div>
      )}

      {error && step !== "error" && <p className="text-sm text-destructive">{error}</p>}
    </div>
  );
}

let pendingQuoteRef: QuoteResponse | null = null;

function StatusMessage({ status, children }: { status: "pending" | "success" | "error"; children: React.ReactNode }) {
  const colors = { pending: "border-primary/30 bg-primary/5 text-primary", success: "border-success/30 bg-success/5 text-success", error: "border-destructive/30 bg-destructive/5 text-destructive" };
  return (
    <div className={`rounded-lg border p-4 text-sm ${colors[status]}`}>
      {status === "pending" && <span className="inline-block animate-spin mr-2">&#9696;</span>}
      {children}
    </div>
  );
}

function formatTokenAmount(raw: string, decimals: number): string {
  const n = BigInt(raw);
  const divisor = 10n ** BigInt(decimals);
  const whole = n / divisor;
  const frac = n % divisor;
  const fracStr = frac.toString().padStart(decimals, "0").slice(0, 6).replace(/0+$/, "");
  return fracStr ? `${whole}.${fracStr}` : whole.toString();
}
