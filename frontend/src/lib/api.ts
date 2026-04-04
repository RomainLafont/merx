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

// Gateway
export function getGatewayBalances(): Promise<{
  depositor: string;
  total: string;
  domains: Array<{ domain: number; chain: string; balance: string }>;
}> {
  return request("/api/gateway/balances");
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
