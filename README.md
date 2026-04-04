# Merx

Chain-abstracted ebook shop built on Circle's CCTP V2 and Arc. Customers pay in USDC from any of 18 supported chains — gasless, with a single signature. Payments settle on Arc in ~5 seconds. The merchant can refund to any chain, and earn yield by supplying USDC to Compound V3.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Frontend (React + Vite)                                        │
│  Shop → Checkout → sign EIP-2612 permit (gasless)               │
│  Dashboard → balances, invoices, supply/withdraw, refund        │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                     Go API server
                           │
          ┌────────────────┼────────────────┐
          │                │                │
    Uniswap API     CCTP V2 + Arc     Compound V3
   (token swaps)    (settlement)       (yield)
```

### Payment (customer → merchant)

```
Customer wallet (any chain)
  │  signs EIP-2612 permit (off-chain, zero gas)
  ▼
Go backend broadcasts payWithPermit
  ▼
ShopPaymaster (source chain)
  │  permit → transferFrom → depositForBurn
  ▼
CCTP V2 Fast Transfer → Arc (~5s)
  ▼
Backend self-relays receiveMessage on Arc
  ▼
USDC in merchant wallet on Arc
```

The customer never pays gas. The backend pays gas on the source chain via the ShopPaymaster contract and on Arc for the CCTP relay.

On chains with Uniswap support, customers can also pay with any token (WETH, UNI, etc.) — the frontend handles the swap via Permit2, then triggers the same gasless USDC payment flow.

### Refund (merchant → customer)

```
Backend calls depositForBurn on Arc → destination chain
  ▼
CCTP V2 attestation (~5s from Arc)
  ▼
Backend self-relays receiveMessage on destination
  ▼
Customer receives USDC
```

Refunds can go to any supported chain, not just the chain where the payment originated.

### Yield (merchant treasury)

```
Arc (USDC) → CCTP → Ethereum Sepolia → CompoundDepositor
  │                                          │
  │  depositForBurn + receiveMessage         │  supply to Compound V3
  │                                          │  (earning yield)
  ▼                                          ▼
Withdraw: Compound → CCTP → Arc        cUSDCv3 balance
```

The merchant can supply idle USDC to Compound V3 for yield, and withdraw back to Arc at any time.

## Why these technologies

| Technology | Role | Why |
|------------|------|-----|
| **CCTP V2** | Cross-chain USDC bridge | Native burn/mint — no wrapped tokens, no liquidity pools. Fast Transfer attests before source finality (~5s). |
| **Arc** | Settlement hub | Sub-second deterministic finality (BFT), no reorgs. Gas paid in USDC — merchant only needs one token. |
| **EIP-2612 Permit** | Gasless payments | Customer signs off-chain, backend broadcasts. Zero gas for the customer. |
| **ShopPaymaster** | Atomic permit + bridge | One contract call: permit → transferFrom → CCTP depositForBurn. Deployed per source chain. |
| **Compound V3** | Yield on idle USDC | Supply/withdraw via CompoundDepositor contract on Ethereum Sepolia. |
| **Uniswap** | Token swaps | Pay with any token — Permit2 + Universal Router swap WETH/UNI/etc. to USDC atomically. |

## Quick start

```bash
# Backend
go run cmd/server/main.go

# Frontend
cd frontend && npm install && npm run dev
```

Open http://localhost:5173 — browse ebooks, connect wallet, pick a chain and pay.

Merchant dashboard at http://localhost:5173/dashboard (connect with merchant wallet).

## Project structure

```
params.go                 Shared addresses, chain config, RPC URLs
registry.yaml             Supported chains, tokens, explorers, CCTP domains
cmd/server/               Go API server
contracts/src/            Solidity: ShopPaymaster, CompoundDepositor
ebooks/                   Downloadable PDFs (served after payment)
frontend/src/pages/       ShopPage, CheckoutPage, DashboardPage
frontend/src/components/  PaymentFlow, ChainSelector, ConnectWallet
frontend/src/lib/         API client, products, chains, formatting
```

## API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/chains` | Supported chains and tokens |
| GET | `/api/pay-tx` | Permit data for gasless payment |
| POST | `/api/pay` | Submit signed permit, broadcast + bridge to Arc |
| GET | `/api/balances` | Shop USDC balance on Arc |
| GET | `/api/merchant/balances` | Balances across all chains + Compound APY |
| POST | `/api/supply` | Supply USDC from Arc to Compound V3 |
| POST | `/api/withdraw` | Withdraw from Compound V3, bridge back to Arc |
| POST | `/api/refund` | Refund to customer on any chain |
| POST | `/api/invoices` | Create invoice |
| GET | `/api/invoices` | List invoices |
| GET | `/api/ebooks/{invoiceId}` | Download purchased ebook PDF |
| POST | `/api/uniswap/quote` | Get swap quote |
| POST | `/api/uniswap/swap` | Build swap transaction |

## Supported chains

18 EVM testnets with CCTP V2 (fast transfer or standard < 10s):

Ethereum Sepolia, Avalanche Fuji, OP Sepolia, Arbitrum Sepolia, Base Sepolia, Polygon Amoy, Unichain Sepolia, Sonic Blaze, Worldchain Sepolia, Sei Atlantic, Linea Sepolia, Codex, Monad, HyperEVM, Ink Sepolia, Plume, EDGE, Morph Hoodi.

Settlement chain: **Arc Testnet** (domain 26).

## Deployed contracts

| Contract | Chain | Address |
|----------|-------|---------|
| ShopPaymaster | Unichain Sepolia | `0xF9b392b25eA1a7671C4badB0E356cc5457AdC47a` |
| ShopPaymaster | Base Sepolia | `0xd94617064C8ca3bfE543A6B0190accB2E41b5Af5` |
| ShopPaymaster | Ethereum Sepolia | `0xb0262c0Cb99329706126Cae0f152C575067e450a` |
| CompoundDepositor | Ethereum Sepolia | `0x832705f381957C8218d7ae8B20A10d510B5AFB75` |

CCTP V2 contracts (same on all testnets): TokenMessengerV2 `0x8FE6...2DAA`, MessageTransmitter `0xE737...E275`.

## Testing

```bash
# Go tests
go test ./...

# Solidity tests
cd contracts && forge test -vvv

# Frontend build
cd frontend && npm run build

# Integration tests (hits live APIs)
INTEGRATION=1 go test ./cmd/server/ -v
```

## Invoice lifecycle

```
paid → bridging → attesting → settled
                                 ↓
                            refunding → refunded
```

Each status transition is tracked with transaction hashes (source chain + Arc settlement + refund). The dashboard auto-refreshes every 10 seconds.
