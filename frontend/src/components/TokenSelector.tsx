import { useState, useRef, useEffect } from "react";
import type { TokenEntry } from "@/types/chain";

interface TokenOption {
  token: TokenEntry;
  balance?: bigint;
  isSwap: boolean; // true = via Uniswap, false = direct USDC
}

interface Props {
  options: TokenOption[];
  selected: string;
  onSelect: (symbol: string) => void;
  loading?: boolean;
  formatBalance: (raw: string, decimals: number) => string;
}

export function TokenSelector({ options, selected, onSelect, loading, formatBalance }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  const current = options.find((o) => o.token.symbol === selected);

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="w-full flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2.5 text-sm text-foreground hover:border-primary/40 transition-colors"
      >
        {current ? (
          <>
            {current.isSwap && <img src="/uniswap.png" alt="" className="h-4 w-4" />}
            <span className="flex-1 text-left font-medium">{current.token.symbol}</span>
            <span className="text-xs text-muted-foreground">
              {loading ? "..." : current.balance !== undefined ? formatBalance(current.balance.toString(), current.token.decimals) : ""}
            </span>
          </>
        ) : (
          <span className="flex-1 text-left text-muted-foreground">Select token...</span>
        )}
        <svg className="h-4 w-4 text-muted-foreground shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
        </svg>
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-full rounded-md border border-border bg-card shadow-lg max-h-64 overflow-y-auto">
          {options.map((o) => (
            <button
              key={o.token.symbol}
              onClick={() => { onSelect(o.token.symbol); setOpen(false); }}
              className={`w-full flex items-center gap-2 px-3 py-2.5 text-sm transition-colors hover:bg-secondary/50 ${
                selected === o.token.symbol
                  ? o.isSwap ? "bg-[#ff007a]/10 text-[#ff007a]" : "bg-primary/10 text-primary"
                  : "text-foreground"
              }`}
            >
              {o.isSwap && <img src="/uniswap.png" alt="" className="h-4 w-4" />}
              {!o.isSwap && <div className="h-4 w-4" />}
              <span className="flex-1 text-left font-medium">{o.token.symbol}</span>
              <span className="text-xs text-muted-foreground">
                {loading ? "..." : o.balance !== undefined ? formatBalance(o.balance.toString(), o.token.decimals) : ""}
              </span>
            </button>
          ))}
          {options.length === 0 && (
            <div className="px-3 py-2.5 text-sm text-muted-foreground">No tokens available</div>
          )}
        </div>
      )}
    </div>
  );
}
