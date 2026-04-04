# Merx

On-chain ebook shop where customers pay in USDC — or swap from any token via Uniswap. Payments are deposited into Circle Gateway via CCTPv2 for a unified cross-chain merchant balance. The shop can refund to any chain instantly.

## Architecture

```
Browser (React SPA :5173)  →  Go API server (:8080)  →  Uniswap Trading API
                                                      →  Circle Gateway API
                                                      →  CCTP V2 Attestation API
```

### Payment flow (customer → merchant)

**Direct USDC:** Customer sends an ERC-20 `transfer` to the merchant address.

**Swap from any token:**
1. Customer picks a token (WETH, UNI, etc.) and gets a quote
2. Signs a Permit2 message (gasless, off-chain)
3. Confirms the swap transaction (USDC lands in customer wallet)
4. Confirms the transfer to the merchant

**After payment — Gateway deposit (backend, automatic):**
1. `USDC.approve(TokenMessengerV2)` on source chain
2. `TokenMessengerV2.depositForBurn()` — burns USDC, emits CCTP message
3. Poll CCTP attestation API until complete
4. `ArcReceiver.relayAndDeposit()` on Arc — mints USDC + deposits into Gateway

### Refund flow (merchant → customer)

```
POST /api/refund  →  allocate Gateway balances  →  sign burn intents
  →  Gateway Forwarding Service  →  customer receives USDC on target chain
```

## Project structure

```
registry.yaml              Token registry (chains, tokens, addresses, RPCs)
params.go                  CCTP domains, USDC addresses, contract addresses
gateway/                   Gateway client, types, EIP-712 signer, tests
uniswap-api/config/        Chain config, YAML loader
uniswap-api/uniswap/       Uniswap Trading API client (quote, swap, approval)
cmd/server/                API server (invoices, uniswap proxy, gateway, CCTP relay)
cmd/refund/                Admin refund CLI
contracts/src/             Solidity: ShopPaymaster, ArcReceiver
contracts/test/            Foundry tests
contracts/script/          Deploy scripts
frontend/                  React + Vite + TypeScript webshop
  src/pages/               ShopPage (catalog), CheckoutPage (payment)
  src/components/          PaymentFlow, ProductCard, Layout, ConnectWallet
  src/lib/                 API client, wagmi config, products, formatting
```

## Quick start

```bash
# 1. Start the API server (requires uniswap-api/config.yaml with API key)
go run cmd/server/main.go

# 2. Start the frontend
cd frontend && npm install && npm run dev
```

Open http://localhost:5173 — browse ebooks, connect MetaMask, pick a chain and token, pay.

### Configuration

**`uniswap-api/config.yaml`** — Uniswap Trading API key:
```yaml
uniswap_api_key: "your-key-from-developers.uniswap.org"
swapper_address: "0xYourAddress"
```

**`registry.yaml`** — Supported chains, tokens, and RPCs:
```yaml
chains:
  - name: Ethereum Sepolia
    chainId: 11155111
    gatewayDomain: 0
    rpc: "https://ethereum-sepolia-rpc.publicnode.com"
    tokens:
      - symbol: USDC
        decimals: 6
        address: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
      - symbol: WETH
        decimals: 18
        address: "0xfff9976782d46cc05630d1f6ebab18b2324d6b14"
```

## API server

```bash
go run cmd/server/main.go
go run cmd/server/main.go --port 3001 --registry registry.yaml
```

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/chains` | Supported chains and tokens (from registry.yaml) |
| POST | `/api/invoices` | Create invoice `{merchantAddress, amount, chainId, description}` |
| GET | `/api/invoices` | List invoices (optional `?merchant=0x...`) |
| GET | `/api/invoices/{id}` | Get invoice by ID |
| POST | `/api/invoices/{id}/pay` | Mark paid `{txHash}`, triggers CCTP→Arc Gateway deposit |
| POST | `/api/uniswap/quote` | Get swap quote `{tokenIn, tokenInChainId, amount, swapper}` |
| POST | `/api/uniswap/approval` | Check token approval |
| POST | `/api/uniswap/swap` | Build swap TX `{quote, signature, permitData}` |
| GET | `/api/gateway/balances` | Merchant's unified Gateway balance across all domains |
| GET | `/api/info` | Gateway domains and contracts |
| GET | `/api/balances` | Shop's Gateway USDC balances (raw) |
| POST | `/api/refund` | Start cross-chain refund `{to, chain, amount}` |
| GET | `/api/refund/{id}` | Poll refund status |

## Testing

```bash
# Unit tests
go test ./...

# Frontend type-check + build
cd frontend && npx tsc --noEmit && npx vite build

# Integration tests (hits live APIs)
INTEGRATION=1 go test ./gateway/ -run TestSmoke -v
INTEGRATION=1 go test ./cmd/server/ -v

# On-chain smoke tests (requires funded wallets)
SMOKE=1 go test ./gateway/ -run TestSelfmintFull -v -timeout 35m
```

## Supported chains (testnet)

| Chain | Chain ID | CCTP Domain | USDC | Swap tokens |
|-------|----------|-------------|------|-------------|
| Ethereum Sepolia | 11155111 | 0 | `0x1c7D...7238` | WETH, UNI |
| Base Sepolia | 84532 | 6 | `0x036C...f7e` | WETH (limited liquidity) |
| Unichain Sepolia | 1301 | 10 | `0x31d0...68f` | WETH |

## Key contracts (same on all EVM testnet chains)

| Contract | Address |
|----------|---------|
| Gateway Wallet | `0x0077777d7EBA4688BDeF3E311b846F25870A19B9` |
| Gateway Minter | `0x0022222ABE238Cc2C7Bb1f21003F0a260052475B` |
| TokenMessengerV2 | `0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA` |
| ArcReceiver | `0x0A4eFeFbB7286D864cDDf6957642b2B11cd58f30` |
