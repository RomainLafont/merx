# Merx Frontend

React + Vite + TypeScript webshop for the Merx ebook store.

## Stack

- **React 19** + **Vite** + **TypeScript**
- **wagmi** + **viem** — wallet connection and on-chain interactions
- **Tailwind CSS v4** — styling
- **TanStack React Query** — server state management

## Pages

- `/` — Ebook catalog (6 products with USDC prices)
- `/checkout/:productId` — Chain selector, token picker, payment flow

## Payment flow

1. **Select chain** — auto-switches MetaMask network
2. **Select token** — USDC (direct transfer) or any token from registry (swap via Uniswap)
3. **Pay** — Direct USDC transfer, or: Permit2 signature → swap → transfer to merchant

## Development

```bash
npm install
npm run dev
```

The Vite dev server runs on `http://localhost:5173` and proxies `/api/*` to the Go backend on `http://localhost:8080`.

## Build

```bash
npm run build     # production build to dist/
npx tsc --noEmit  # type-check only
```
