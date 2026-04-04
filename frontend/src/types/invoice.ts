export interface Invoice {
  id: string;
  merchantAddress: string;
  amount: string; // base units (6 decimals)
  amountHuman: string; // e.g. "100.00"
  chainId: number;
  description: string;
  payerAddress?: string;
  status: "pending" | "paid" | "bridging" | "attesting" | "settled";
  txHash?: string;
  arcTxHash?: string;
  refundArcTxHash?: string;
  refundTxHash?: string;
  refundChainId?: number;
  createdAt: string;
  paidAt?: string;
  settledAt?: string;
}

export interface CreateInvoiceRequest {
  merchantAddress: string;
  amount: string; // human-readable USDC
  chainId: number;
  description: string;
}
