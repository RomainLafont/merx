import type { ReactNode } from "react";
import { Link, useLocation } from "react-router-dom";
import { useAccount } from "wagmi";
import { ConnectWallet } from "./ConnectWallet";
import { MERCHANT_ADDRESS } from "@/lib/constants";

export function Layout({ children }: { children: ReactNode }) {
  const location = useLocation();
  const { address } = useAccount();
  const isMerchant = address?.toLowerCase() === MERCHANT_ADDRESS.toLowerCase();

  return (
    <div className="min-h-screen flex flex-col">
      <header className="border-b border-border px-6 py-4 flex items-center justify-between">
        <div className="flex items-center gap-6">
          <Link to="/" className="flex items-center gap-2 text-xl font-bold text-foreground">
            <img src="/logo.png" alt="Merx" className="h-8 w-8 rounded-full" />
            Merx
          </Link>
          <nav className="flex gap-4">
            <Link
              to="/"
              className={`text-sm transition-colors ${
                location.pathname === "/" ? "text-primary" : "text-muted-foreground hover:text-foreground"
              }`}
            >
              Shop
            </Link>
            {isMerchant && (
              <Link
                to="/dashboard"
                className={`text-sm transition-colors ${
                  location.pathname === "/dashboard" ? "text-primary" : "text-muted-foreground hover:text-foreground"
                }`}
              >
                Dashboard
              </Link>
            )}
          </nav>
        </div>
        <ConnectWallet />
      </header>
      <main className="flex-1 p-6">{children}</main>
    </div>
  );
}
