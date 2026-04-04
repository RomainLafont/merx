import { useState } from "react";
import { useParams, Link } from "react-router-dom";
import { useAccount } from "wagmi";
import { useQuery } from "@tanstack/react-query";
import { getProduct } from "@/lib/products";
import { getChains } from "@/lib/api";
import { PaymentFlow } from "@/components/PaymentFlow";
import { ConnectWallet } from "@/components/ConnectWallet";
import type { ChainInfo } from "@/types/chain";

import { MERCHANT_ADDRESS } from "@/lib/constants";

export function CheckoutPage() {
  const { productId } = useParams<{ productId: string }>();
  const { address, isConnected } = useAccount();
  const product = getProduct(productId ?? "");

  const [paid, setPaid] = useState(false);
  const [error, setError] = useState("");

  const { data: chains } = useQuery<ChainInfo[]>({
    queryKey: ["chains"],
    queryFn: getChains,
  });

  if (!product) {
    return (
      <div className="max-w-lg mx-auto text-center py-12 space-y-4">
        <p className="text-muted-foreground">Product not found.</p>
        <Link to="/" className="text-primary hover:underline text-sm">
          Back to shop
        </Link>
      </div>
    );
  }

  return (
    <div className="max-w-lg mx-auto space-y-6">
      <Link
        to="/"
        className="text-sm text-muted-foreground hover:text-foreground transition-colors"
      >
        &larr; Back to shop
      </Link>

      {/* Product summary */}
      <div className="rounded-lg border border-border bg-card p-6 flex gap-4 items-center">
        <span className="text-5xl">{product.cover}</span>
        <div className="flex-1">
          <h1 className="text-xl font-bold">{product.title}</h1>
          <p className="text-sm text-muted-foreground">{product.author}</p>
        </div>
        <span className="text-2xl font-bold text-primary">
          {product.price} USDC
        </span>
      </div>

      {/* Payment section */}
      <div className="rounded-lg border border-border bg-card p-6 space-y-4">
        <h2 className="text-lg font-semibold">Payment</h2>

        {!isConnected ? (
          <div className="text-center space-y-3 py-4">
            <p className="text-muted-foreground text-sm">
              Connect your wallet to proceed
            </p>
            <ConnectWallet />
          </div>
        ) : (
          <PaymentFlow
            chains={chains ?? []}
            product={product}
            merchantAddress={MERCHANT_ADDRESS}
            onPaid={() => setPaid(true)}
          />
        )}

        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
    </div>
  );
}
