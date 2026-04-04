import { useState, useEffect, useCallback } from "react";
import {
  useAccount,
  useReadContract,
  useSendTransaction,
  useWaitForTransactionReceipt,
  useWriteContract,
  useSignTypedData,
  useSwitchChain,
  useChainId,
} from "wagmi";
import { parseAbi, erc20Abi, type Hex } from "viem";
import type { Invoice } from "@/types/invoice";
import type { QuoteResponse } from "@/types/uniswap";
import type { ChainInfo, TokenEntry } from "@/types/chain";
import type { Product } from "@/lib/products";
import { getQuote, buildSwap, payInvoice } from "@/lib/api";
import { formatUSDC } from "@/lib/format";

const ERC20_ABI = parseAbi([
  "function transfer(address to, uint256 amount) returns (bool)",
]);

const PERMIT2 = "0x000000000022D473030F116dDEE9F6B43aC78BA3" as Hex;
const MAX_UINT256 = 2n ** 256n - 1n;

type Step = "select-chain" | "select-token" | "quote" | "approving-permit2" | "signing-permit" | "swapping" | "transferring" | "done" | "error";

interface Props {
  invoice: Invoice | null;
  chains: ChainInfo[];
  product: Product;
  creating: boolean;
  onSelectChain: (chainId: number) => void;
  onReset: () => void;
  onPaid: () => void;
}

export function PaymentFlow({ invoice, chains, product, creating, onSelectChain, onReset, onPaid }: Props) {
  const { address } = useAccount();
  const currentChainId = useChainId();
  const { switchChain } = useSwitchChain();
  const [step, setStep] = useState<Step>("select-chain");
  const [selectedChainId, setSelectedChainId] = useState(0);
  const [selectedSymbol, setSelectedSymbol] = useState("USDC");
  const [quote, setQuote] = useState<QuoteResponse | null>(null);
  const [error, setError] = useState("");
  const [loadingQuote, setLoadingQuote] = useState(false);

  const chain = chains.find((c) => c.chainId === selectedChainId);
  const swapTokens = chain?.tokens.filter((t) => t.symbol !== "USDC") ?? [];
  const selectedToken: TokenEntry | undefined =
    selectedSymbol === "USDC"
      ? chain?.tokens.find((t) => t.symbol === "USDC")
      : chain?.tokens.find((t) => t.symbol === selectedSymbol);
  const usdcToken = chain?.tokens.find((t) => t.symbol === "USDC");
  const isDirectUSDC = selectedSymbol === "USDC";

  // When invoice is created (chain selected), sync chainId and move to token selection
  useEffect(() => {
    if (invoice && step === "select-chain") {
      setSelectedChainId(invoice.chainId);
      setStep("select-token");
    }
  }, [invoice, step]);

  // Read balance of selected token
  const { data: tokenBalance, isLoading: balanceLoading, error: balanceError } = useReadContract({
    address: (selectedToken?.address ?? undefined) as Hex | undefined,
    abi: erc20Abi,
    functionName: "balanceOf",
    args: address ? [address] : undefined,
    chainId: selectedChainId || undefined,
    query: { enabled: !!address && !!selectedToken?.address && selectedChainId > 0 },
  });

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
    hash: permit2ApproveHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 4_000,
  });

  // Direct USDC transfer
  const { writeContract, data: transferHash, isPending: transferPending } = useWriteContract();
  const { isSuccess: transferConfirmed, isError: transferReverted } = useWaitForTransactionReceipt({
    hash: transferHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 4_000,
  });

  // Permit2 signature
  const { signTypedData, data: permitSignature, isPending: signPending, error: signError } = useSignTypedData();

  // Swap TX
  const { sendTransaction: sendSwap, data: swapHash, isPending: swapPending } = useSendTransaction();
  const { isSuccess: swapConfirmed, isError: swapReverted } = useWaitForTransactionReceipt({
    hash: swapHash, chainId: selectedChainId || undefined, confirmations: 1, pollingInterval: 4_000,
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

  // After Permit2 ERC-20 approval confirmed, proceed to permit signature flow
  useEffect(() => {
    if (permit2ApproveConfirmed && step === "approving-permit2") {
      proceedToPermitSign();
    }
  }, [permit2ApproveConfirmed, step]);

  // After permit signed �� swap
  useEffect(() => {
    if (permitSignature && step === "signing-permit") { executeSwapWithPermit(permitSignature); }
  }, [permitSignature, step]);

  // After swap confirmed → auto-trigger USDC transfer to merchant
  useEffect(() => {
    if (swapConfirmed && step === "swapping") {
      doTransfer();
    }
  }, [swapConfirmed, step]);

  // After USDC transfer confirmed → mark paid
  useEffect(() => {
    if (transferConfirmed && transferHash && invoice) {
      payInvoice(invoice.id, transferHash).then(() => { setStep("done"); onPaid(); });
    }
  }, [transferConfirmed, transferHash, invoice]);

  // --- Actions ---

  function handleChainSelect(chainId: number) {
    setSelectedChainId(chainId);
    setSelectedSymbol("USDC");
    setQuote(null);
    setError("");
    // Auto-switch wallet network
    if (currentChainId !== chainId) {
      switchChain({ chainId });
    }
    // Create invoice for this chain
    onSelectChain(chainId);
  }

  function handleChangeChain() {
    setStep("select-chain");
    setSelectedChainId(0);
    setSelectedSymbol("USDC");
    setQuote(null);
    setError("");
    onReset();
  }

  const fetchQuote = useCallback(async () => {
    if (!address || !selectedToken || isDirectUSDC || !invoice) return;
    setLoadingQuote(true);
    setError("");
    try {
      const resp = await getQuote({ tokenIn: selectedToken.address, tokenInChainId: selectedChainId, amount: invoice.amount, swapper: address });
      setQuote(resp);
      setStep("quote");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to get quote");
    } finally {
      setLoadingQuote(false);
    }
  }, [address, selectedToken, isDirectUSDC, invoice, selectedChainId]);

  function doTransfer() {
    if (!usdcToken || !invoice) return;
    setStep("transferring");
    writeContract({ address: usdcToken.address as Hex, abi: ERC20_ABI, functionName: "transfer", args: [invoice.merchantAddress as Hex, BigInt(invoice.amount)] });
  }

  async function handlePayWithSwap() {
    if (!address || !selectedToken || !invoice) { setError("Missing data."); setStep("error"); return; }

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
    if (!address || !selectedToken || !invoice) { setError("Missing data."); setStep("error"); return; }
    setStep("signing-permit");
    try {
      const freshResp = await getQuote({ tokenIn: selectedToken.address, tokenInChainId: selectedChainId, amount: invoice.amount, swapper: address });
      setQuote(freshResp);
      if (!freshResp.permitData) { await executeSwapWithPermit(undefined, freshResp); return; }
      pendingQuoteRef = freshResp;
      const pd = freshResp.permitData;
      signTypedData(
        { domain: { name: pd.domain.name as string, chainId: Number(pd.domain.chainId), verifyingContract: pd.domain.verifyingContract as Hex }, types: pd.types as Record<string, Array<{ name: string; type: string }>>, primaryType: "PermitSingle", message: pd.values as Record<string, unknown> },
        { onError(err) { setError(`Permit failed: ${err.message}`); setStep("error"); } },
      );
    } catch (err) { setError(err instanceof Error ? err.message : "Failed to prepare swap"); setStep("error"); }
  }

  async function executeSwapWithPermit(signature?: string, quoteOverride?: QuoteResponse) {
    const activeQuote = quoteOverride ?? pendingQuoteRef;
    if (!activeQuote) { setError("No quote available."); setStep("error"); return; }
    setStep("swapping");
    try {
      const swapResp = await buildSwap({ quote: activeQuote.quote, ...(signature ? { signature } : {}), ...(activeQuote.permitData ? { permitData: activeQuote.permitData } : {}) });
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
      {/* Step 1: Chain selection */}
      {step === "select-chain" && (
        <div className="space-y-3">
          <label className="block text-sm text-muted-foreground">Select network</label>
          <div className="flex flex-wrap gap-2">
            {chains.map((c) => (
              <button
                key={c.chainId}
                onClick={() => handleChainSelect(c.chainId)}
                disabled={creating}
                className="rounded-md border border-border px-4 py-2.5 text-sm font-medium text-muted-foreground hover:text-foreground hover:border-primary/40 transition-colors disabled:opacity-50"
              >
                {c.name}
              </button>
            ))}
          </div>
          {creating && <p className="text-xs text-muted-foreground">Switching network & preparing...</p>}
        </div>
      )}

      {/* Step 2: Token selection */}
      {step === "select-token" && chain && (
        <div className="space-y-4">
          {/* Current chain + change button */}
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">Network: <span className="text-foreground font-medium">{chain.name}</span></span>
            <button onClick={handleChangeChain} className="text-xs text-primary hover:underline">Change</button>
          </div>

          {/* Token list */}
          <div className="space-y-2">
            <label className="block text-sm text-muted-foreground">Pay with</label>
            <div className="flex flex-wrap gap-2">
              <button
                onClick={() => setSelectedSymbol("USDC")}
                className={`rounded-md border px-4 py-2.5 text-sm font-medium transition-colors ${
                  selectedSymbol === "USDC" ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:text-foreground"
                }`}
              >
                USDC
              </button>
              {swapTokens.map((t) => (
                <button
                  key={t.symbol}
                  onClick={() => setSelectedSymbol(t.symbol)}
                  className={`rounded-md border px-4 py-2.5 text-sm font-medium transition-colors ${
                    selectedSymbol === t.symbol ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:text-foreground"
                  }`}
                >
                  {t.symbol}
                </button>
              ))}
            </div>
          </div>

          {/* Balance */}
          {selectedToken && (
            <div className="rounded-md border border-border bg-secondary/50 px-3 py-2 flex justify-between text-sm">
              <span className="text-muted-foreground">Your {selectedSymbol} balance</span>
              <span className="font-medium">
                {balanceLoading ? "Loading..." : balanceError ? "Failed to load" : tokenBalance !== undefined ? `${formatTokenAmount(tokenBalance.toString(), selectedToken.decimals)} ${selectedSymbol}` : "\u2014"}
              </span>
            </div>
          )}

          {/* Action button */}
          {isDirectUSDC ? (
            <button
              onClick={doTransfer}
              disabled={transferPending}
              className="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors"
            >
              {transferPending ? "Confirm in wallet..." : `Pay ${product.price} USDC`}
            </button>
          ) : (
            <button
              onClick={fetchQuote}
              disabled={loadingQuote}
              className="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors"
            >
              {loadingQuote ? "Getting quote..." : `Get ${selectedSymbol} Quote`}
            </button>
          )}
        </div>
      )}

      {/* Step 3: Quote display */}
      {step === "quote" && quote && selectedToken && chain && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">Network: <span className="text-foreground font-medium">{chain.name}</span></span>
            <button onClick={handleChangeChain} className="text-xs text-primary hover:underline">Change</button>
          </div>

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

          <p className="text-xs text-muted-foreground text-center">One signature + swap + transfer to merchant.</p>

          <div className="flex gap-2">
            <button onClick={() => { setStep("select-token"); setQuote(null); }} className="flex-1 rounded-md border border-border px-4 py-2 text-sm text-muted-foreground hover:text-foreground transition-colors">Back</button>
            <button onClick={handlePayWithSwap} className="flex-1 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 transition-colors">Swap & Pay</button>
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
      {step === "done" && (
        <StatusMessage status="success">Payment confirmed! Transaction: {(swapHash ?? transferHash)?.slice(0, 10)}...</StatusMessage>
      )}
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
