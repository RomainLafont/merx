import { useState, useRef, useEffect } from "react";
import type { ChainInfo } from "@/types/chain";
import { chainIcon } from "@/lib/chainIcons";

interface Props {
  chains: ChainInfo[];
  selected: ChainInfo | undefined;
  onSelect: (chainId: number) => void;
  disabled?: boolean;
}

export function ChainSelector({ chains, selected, onSelect, disabled }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click
  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, []);

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
        className="w-full flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground hover:border-primary/40 transition-colors disabled:opacity-50"
      >
        {selected ? (
          <>
            {chainIcon(selected.chainId) && (
              <img src={chainIcon(selected.chainId)} alt="" className="h-5 w-5 rounded-full" />
            )}
            <span className="flex-1 text-left font-medium">{selected.name}</span>
          </>
        ) : (
          <span className="flex-1 text-left text-muted-foreground">Select network...</span>
        )}
        <svg className="h-4 w-4 text-muted-foreground shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
        </svg>
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-full rounded-md border border-border bg-card shadow-lg max-h-64 overflow-y-auto">
          {chains.map((c) => (
            <button
              key={c.chainId}
              onClick={() => {
                onSelect(c.chainId);
                setOpen(false);
              }}
              className={`w-full flex items-center gap-2 px-3 py-2 text-sm transition-colors hover:bg-secondary/50 ${
                selected?.chainId === c.chainId ? "bg-primary/10 text-primary" : "text-foreground"
              }`}
            >
              {chainIcon(c.chainId) ? (
                <img src={chainIcon(c.chainId)} alt="" className="h-5 w-5 rounded-full" />
              ) : (
                <div className="h-5 w-5 rounded-full bg-muted" />
              )}
              <span className="flex-1 text-left">{c.name}</span>
              {!c.uniswapSupported && (
                <span className="text-xs text-muted-foreground">USDC only</span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
