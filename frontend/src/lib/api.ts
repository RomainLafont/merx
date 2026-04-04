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
  balances: Array<{ chain: string; chainId: number; balance: string }>;
  compound: string;
  compoundAPY: string;
}> {
  return request("/api/merchant/balances");
}

export function listInvoices(): Promise<Invoice[]> {
  return request("/api/invoices");
}

// Sweep (bridge to Sepolia + stake to Compound)
export function sweep(amount: string): Promise<{ txHash: string }> {
  return request("/api/sweep", {
    method: "POST",
    body: JSON.stringify({ amount }),
  });
}

// Gasless USDC payment (permit + ShopPaymaster)
export function getPayTx(chainId: number, amount: string): Promise<{
  chain_id: number;
  amount: string;
  deadline: string;
  permit: {
    token: string;
    spender: string;
    domain: {
      name: string;
      version: string;
      chain_id: number;
      verifying_contract: string;
    };
  };
}> {
  return request(`/api/pay-tx?chain_id=${chainId}&amount=${amount}`);
}

export function submitPay(req: {
  owner: string;
  chain_id: number;
  amount: string;
  deadline: string;
  signature: string;
  description: string;
}): Promise<{ tx_hash: string; chain_id: number; invoice_id: string }> {
  return request("/api/pay", {
    method: "POST",
    body: JSON.stringify(req),
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
