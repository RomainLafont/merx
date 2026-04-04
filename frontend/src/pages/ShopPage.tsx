import { useAccount } from "wagmi";
import { useQuery } from "@tanstack/react-query";
import { products } from "@/lib/products";
import { ProductCard } from "@/components/ProductCard";
import { listInvoices } from "@/lib/api";

export function ShopPage() {
  const { address } = useAccount();

  const { data: invoices } = useQuery({
    queryKey: ["invoices-all"],
    queryFn: listInvoices,
    refetchInterval: 10_000,
  });

  // Find purchased (non-refunded) product IDs for the connected user.
  const purchasedProducts = new Set<string>();
  const purchaseInvoiceId = new Map<string, string>(); // productId → invoiceId
  if (invoices && address) {
    for (const inv of invoices) {
      if (
        inv.productId &&
        inv.payerAddress?.toLowerCase() === address.toLowerCase() &&
        !inv.refundTxHash && !inv.refundArcTxHash
      ) {
        purchasedProducts.add(inv.productId);
        purchaseInvoiceId.set(inv.productId, inv.id);
      }
    }
  }

  return (
    <div className="max-w-5xl mx-auto space-y-6">
      <div className="text-center space-y-2">
        <h1 className="text-3xl font-bold">Merx Bookshop</h1>
        <p className="text-muted-foreground">
          Premium crypto & Web3 ebooks — pay with USDC or swap from any asset
        </p>
      </div>
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        {products.map((p) => (
          <ProductCard
            key={p.id}
            product={p}
            purchased={purchasedProducts.has(p.id)}
            invoiceId={purchaseInvoiceId.get(p.id)}
          />
        ))}
      </div>
    </div>
  );
}
