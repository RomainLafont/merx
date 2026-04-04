import { Link } from "react-router-dom";
import type { Product } from "@/lib/products";

interface Props {
  product: Product;
  purchased?: boolean;
  invoiceId?: string;
}

export function ProductCard({ product, purchased, invoiceId }: Props) {
  return (
    <div className="rounded-lg border border-border bg-card p-5 flex flex-col gap-3 hover:border-primary/40 transition-colors">
      <div className="text-6xl text-center py-4">{product.cover}</div>
      <div className="flex-1 space-y-1">
        <h3 className="font-semibold text-foreground leading-tight">
          {product.title}
        </h3>
        <p className="text-sm text-muted-foreground">{product.author}</p>
        <p className="text-xs text-muted-foreground leading-relaxed mt-2">
          {product.description}
        </p>
      </div>
      <div className="flex items-center justify-between pt-2">
        <span className="text-lg font-bold text-primary">
          {product.price} USDC
        </span>
        {purchased && invoiceId ? (
          <a
            href={`/api/ebooks/${invoiceId}`}
            download
            className="rounded-md bg-success px-4 py-2 text-sm font-medium text-white hover:bg-success/90 transition-colors flex items-center gap-1.5"
          >
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="currentColor" className="w-4 h-4">
              <path d="M2.75 14A1.75 1.75 0 0 1 1 12.25v-2.5a.75.75 0 0 1 1.5 0v2.5c0 .138.112.25.25.25h10.5a.25.25 0 0 0 .25-.25v-2.5a.75.75 0 0 1 1.5 0v2.5A1.75 1.75 0 0 1 13.25 14H2.75Z" />
              <path d="M7.25 7.689V2a.75.75 0 0 1 1.5 0v5.689l1.97-1.969a.749.749 0 1 1 1.06 1.06l-3.25 3.25a.749.749 0 0 1-1.06 0L4.22 6.78a.749.749 0 1 1 1.06-1.06l1.97 1.969Z" />
            </svg>
            Access Ebook
          </a>
        ) : (
          <Link
            to={`/checkout/${product.id}`}
            className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 transition-colors"
          >
            Buy Now
          </Link>
        )}
      </div>
    </div>
  );
}
