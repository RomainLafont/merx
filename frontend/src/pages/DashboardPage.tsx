import { useState } from "react";
import { Navigate } from "react-router-dom";
import { useAccount } from "wagmi";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getMerchantBalances, listInvoices, sweep, getChains } from "@/lib/api";
import { shortenAddress, formatUSDC } from "@/lib/format";
import { chainName } from "@/lib/chains";
import type { ChainInfo } from "@/types/chain";
import { chainIcon } from "@/lib/chainIcons";
import { MERCHANT_ADDRESS } from "@/lib/constants";
import type { Invoice } from "@/types/invoice";

const ARC_CHAIN_ID = 5042002;

export function DashboardPage() {
  const { address } = useAccount();
  const isMerchant = address?.toLowerCase() === MERCHANT_ADDRESS.toLowerCase();

  if (!isMerchant) {
    return <Navigate to="/" replace />;
  }
  const queryClient = useQueryClient();

  const { data: balances, isLoading: balancesLoading } = useQuery({
    queryKey: ["merchant-balances"],
    queryFn: getMerchantBalances,
    refetchInterval: 10_000,
  });

  const { data: invoices, isLoading: invoicesLoading } = useQuery({
    queryKey: ["invoices-all"],
    queryFn: listInvoices,
    refetchInterval: 10_000,
  });

  const { data: chainsData } = useQuery<ChainInfo[]>({
    queryKey: ["chains"],
    queryFn: getChains,
  });

  function explorerTxUrl(chainId: number, txHash: string): string | undefined {
    const c = chainsData?.find((ch) => ch.chainId === chainId);
    return c?.explorer ? `${c.explorer}/tx/${txHash}` : undefined;
  }

  const [sweeping, setSweeping] = useState(false);
  const [sweepResult, setSweepResult] = useState<string | null>(null);
  const [sweepError, setSweepError] = useState("");
  const [sweepAmount, setSweepAmount] = useState("");

  const totalHuman = balances ? formatUSDC(balances.total) : "0";
  const arcBalance = balances?.balances.find((b) => b.chainId === ARC_CHAIN_ID);
  const arcAmount = arcBalance?.balance ?? "0";
  const hasArcFunds = arcAmount !== "0";

  // Convert human input (e.g. "1.5") to base units (e.g. "1500000").
  const sweepBaseUnits = sweepAmount
    ? String(Math.floor(parseFloat(sweepAmount) * 1_000_000))
    : "0";
  const sweepValid = parseFloat(sweepAmount) > 0 && BigInt(sweepBaseUnits) <= BigInt(arcAmount);

  async function handleSweep() {
    if (!sweepValid) return;
    setSweeping(true);
    setSweepError("");
    setSweepResult(null);
    try {
      const res = await sweep(sweepBaseUnits);
      setSweepResult(res.txHash);
      setSweepAmount("");
      setTimeout(() => queryClient.invalidateQueries({ queryKey: ["merchant-balances"] }), 3000);
    } catch (err) {
      setSweepError(err instanceof Error ? err.message : "Sweep failed");
    } finally {
      setSweeping(false);
    }
  }

  return (
    <div className="max-w-4xl mx-auto space-y-8">
      <div className="text-center space-y-1">
        <h1 className="text-3xl font-bold">Merchant Dashboard</h1>
        {balances && (
          <p className="text-sm text-muted-foreground font-mono">
            {shortenAddress(balances.merchant)}
          </p>
        )}
      </div>

      {/* Total balance */}
      <div className="rounded-lg border border-border bg-card p-6 text-center">
        <p className="text-sm text-muted-foreground mb-1">Total USDC balance</p>
        <p className="text-4xl font-bold text-primary">
          {balancesLoading ? "..." : `${totalHuman} USDC`}
        </p>
      </div>

      {/* Per-chain balances */}
      <div className="space-y-2">
        <h2 className="text-lg font-semibold">Balances by chain</h2>
        {balancesLoading ? (
          <p className="text-muted-foreground text-sm">Loading...</p>
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {balances?.balances.map((b) => {
              const human = formatUSDC(b.balance);
              const isZero = b.balance === "0";
              return (
                <div
                  key={b.chainId}
                  className="rounded-lg border border-border bg-card p-4 flex items-center justify-between"
                >
                  <div className="flex items-center gap-3">
                    {chainIcon(b.chainId) && <img src={chainIcon(b.chainId)} alt="" className="h-6 w-6 rounded-full" />}
                    <div>
                      <p className="font-medium text-sm">{b.chain}</p>
                      <p className="text-xs text-muted-foreground">Chain ID: {b.chainId}</p>
                    </div>
                  </div>
                  <span className={`text-lg font-bold ${isZero ? "text-muted-foreground" : "text-foreground"}`}>
                    {human}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Compound V3 balance */}
      <div className="rounded-lg border border-border bg-card p-6">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm text-muted-foreground mb-1">Compound V3 Vault (Ethereum Sepolia)</p>
            <p className="text-2xl font-bold text-primary">
              {balancesLoading ? "..." : `${formatUSDC(balances?.compound ?? "0")} USDC`}
            </p>
          </div>
          <div className="text-right">
            <p className="text-xs text-muted-foreground">Earning yield</p>
            <p className="text-xs text-success font-medium">cUSDCv3</p>
          </div>
        </div>
      </div>

      {/* Sweep to Compound */}
      <div className="rounded-lg border border-border bg-card p-6 space-y-4">
        <div>
          <h2 className="text-lg font-semibold">Stake to Compound V3</h2>
          <p className="text-sm text-muted-foreground">
            Bridge USDC from Arc to Ethereum Sepolia and supply to the Compound lending vault.
          </p>
        </div>

        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-muted-foreground">Available on Arc</p>
              <p className="text-xl font-bold">
                {balancesLoading ? "..." : `${formatUSDC(arcAmount)} USDC`}
              </p>
            </div>
          </div>

          <div className="space-y-3">
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">Sweep amount</span>
              <span className="font-mono font-bold">
                {sweepAmount ? `${sweepAmount} USDC` : "0 USDC"}
              </span>
            </div>
            <input
              type="range"
              min="0"
              max={arcAmount}
              step="10000"
              value={sweepAmount ? String(Math.floor(parseFloat(sweepAmount) * 1_000_000)) : "0"}
              onChange={(e) => {
                const val = parseInt(e.target.value);
                setSweepAmount(val > 0 ? (val / 1_000_000).toFixed(2) : "");
              }}
              disabled={sweeping || !hasArcFunds}
              className="w-full h-2 rounded-full appearance-none cursor-pointer bg-border accent-primary disabled:opacity-50 disabled:cursor-not-allowed"
            />
            <div className="flex items-center gap-3">
              <div className="relative flex-1">
                <input
                  type="number"
                  step="0.01"
                  min="0"
                  placeholder="0.00"
                  value={sweepAmount}
                  onChange={(e) => setSweepAmount(e.target.value)}
                  disabled={sweeping || !hasArcFunds}
                  className="w-full rounded-md border border-border bg-background px-3 py-2.5 text-sm font-mono placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary/50 disabled:opacity-50"
                />
                <span className="absolute right-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">
                  USDC
                </span>
              </div>
              <button
                onClick={() => setSweepAmount(formatUSDC(arcAmount))}
                disabled={sweeping || !hasArcFunds}
                className="rounded-md border border-border px-3 py-2.5 text-xs font-medium text-muted-foreground hover:bg-accent disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                Max
              </button>
              <button
                onClick={handleSweep}
                disabled={sweeping || !sweepValid || balancesLoading}
                className="rounded-md bg-primary px-5 py-2.5 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {sweeping ? "Sweeping..." : "Sweep"}
              </button>
            </div>
          </div>
        </div>

        {sweepResult && (
          <div className="rounded-md border border-success/30 bg-success/5 p-3 text-sm text-success">
            Sweep initiated!{" "}
            <a href={`https://testnet.explorer.arc.network/tx/${sweepResult}`} target="_blank" rel="noopener noreferrer" className="underline font-mono hover:opacity-80">
              View transaction
            </a>
            <br />
            <span className="text-xs">CCTP bridging in progress. Funds will appear in Compound after attestation (~5 min).</span>
          </div>
        )}
        {sweepError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {sweepError}
          </div>
        )}
      </div>

      {/* Recent invoices */}
      <div className="space-y-2">
        <h2 className="text-lg font-semibold">Recent invoices</h2>
        {invoicesLoading ? (
          <p className="text-muted-foreground text-sm">Loading...</p>
        ) : !invoices || invoices.length === 0 ? (
          <p className="text-muted-foreground text-sm">No invoices yet.</p>
        ) : (
          <div className="space-y-2">
            {invoices
              .sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
              .slice(0, 10)
              .map((inv) => (
                <InvoiceRow key={inv.id} invoice={inv} explorerTxUrl={explorerTxUrl} />
              ))}
          </div>
        )}
      </div>
    </div>
  );
}

function InvoiceRow({ invoice, explorerTxUrl }: { invoice: Invoice; explorerTxUrl: (chainId: number, txHash: string) => string | undefined }) {
  const txUrl = invoice.txHash ? explorerTxUrl(invoice.chainId, invoice.txHash) : undefined;

  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 flex items-center justify-between text-sm">
      <div className="flex items-center gap-3">
        <span
          className={`text-xs px-2 py-0.5 rounded-full font-medium ${
            invoice.status === "paid"
              ? "bg-success/20 text-success"
              : "bg-primary/20 text-primary"
          }`}
        >
          {invoice.status}
        </span>
        <span className="text-muted-foreground">{invoice.description}</span>
      </div>
      <div className="flex items-center gap-4">
        <span className="flex items-center gap-1 text-muted-foreground text-xs">
          {chainIcon(invoice.chainId) && <img src={chainIcon(invoice.chainId)} alt="" className="h-3.5 w-3.5 rounded-full" />}
          {chainName(invoice.chainId)}
        </span>
        <span className="font-semibold">{invoice.amountHuman} USDC</span>
        {invoice.txHash && (
          txUrl ? (
            <a href={txUrl} target="_blank" rel="noopener noreferrer" className="font-mono text-xs text-primary hover:underline">{shortenAddress(invoice.txHash)}</a>
          ) : (
            <span className="font-mono text-xs text-muted-foreground">{shortenAddress(invoice.txHash)}</span>
          )
        )}
      </div>
    </div>
  );
}
