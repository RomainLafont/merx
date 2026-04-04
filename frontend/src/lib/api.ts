import type { ChainInfo } from "@/types/chain";
import type { Invoice, CreateInvoiceRequest } from "@/types/invoice";
import type {
  QuoteRequest,
  QuoteResponse,
  SwapResponse,
} from "@/types/uniswap";

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const data = await res.json();
  if (!res.ok) {
    throw new Error(data.detail || data.error || `HTTP ${res.status}`);
  }
  return data as T;
}

// Chains
export function getChains(): Promise<ChainInfo[]> {
  return request("/api/chains");
}

// Invoices
export function createInvoice(req: CreateInvoiceRequest): Promise<Invoice> {
  return request("/api/invoices", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export function payInvoice(
  id: string,
  txHash: string
): Promise<Invoice> {
  return request(`/api/invoices/${id}/pay`, {
    method: "POST",
    body: JSON.stringify({ txHash }),
  });
}

// Merchant dashboard
export function getMerchantBalances(): Promise<{
  merchant: string;
  total: string;
  balances: Array<{ chain: string; chainId: number; balance: string; nativeBalance: string }>;
  compound: string;
  compoundAPY: string;
}> {
  return request("/api/merchant/balances");
}

export function listInvoices(): Promise<Invoice[]> {
  return request("/api/invoices");
}

// Get calldata for depositForBurnWithHook (customer executes this on-chain)
export function getPayTx(chainId: number, amount: string): Promise<{
  to: string;
  data: string;
  chain_id: number;
  value: string;
  maxFee: string;
  approval: { spender: string; token: string; amount: string };
}> {
  return request(`/api/pay-tx?chain_id=${chainId}&amount=${amount}`);
}

// Report payment tx to backend (customer already executed the tx)
export function submitPay(req: {
  txHash: string;
  chainId: number;
  amount: string;
  owner: string;
  description: string;
  productId: string;
}): Promise<{ txHash: string; invoiceId: string }> {
  return request("/api/pay", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// Compound V3 supply (bridge from Arc to Sepolia + supply)
export function supply(amount: string): Promise<{ txHash: string }> {
  return request("/api/supply", {
    method: "POST",
    body: JSON.stringify({ amount }),
  });
}

// Compound V3 withdraw (withdraw from Compound + bridge back to Arc)
export function withdraw(amount: string): Promise<{ txHash: string }> {
  return request("/api/withdraw", {
    method: "POST",
    body: JSON.stringify({ amount }),
  });
}

// Refund
export function refund(req: {
  invoiceId: string;
  to: string;
  chainId: number;
  amount: string;
}): Promise<{ txHash: string }> {
  return request("/api/refund", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

// Uniswap
export function getQuote(req: QuoteRequest): Promise<QuoteResponse> {
  return request("/api/uniswap/quote", {
    method: "POST",
    body: JSON.stringify(req),
  });
}

export function buildSwap(req: {
  quote: unknown;
  signature?: string;
  permitData?: unknown;
}): Promise<SwapResponse> {
  return request("/api/uniswap/swap", {
    method: "POST",
    body: JSON.stringify(req),
  });
}
