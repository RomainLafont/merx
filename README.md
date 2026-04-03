# Merx

On-chain shop POC built on Circle Gateway. A customer pays in USDC from any supported chain (gasless via ERC-3009), the shop accumulates a unified Gateway balance, sweeps yield to a vault on Base, and can refund cross-chain instantly.

## Client Side

### [uniswap-api](./uniswap-api/)

Go client for the [Uniswap Trading API](https://api-docs.uniswap.org/introduction). Get token prices in USDC and execute swaps on supported testnets (Ethereum Sepolia, Base Sepolia, Unichain Sepolia).

See [uniswap-api/README.md](./uniswap-api/README.md) for setup and usage.

### Progress

- [x] Uniswap API client (quote, swap, approval)
- [x] Multi-chain testnet support
- [x] Unit + integration tests
- [x] CLI tool
- [ ] Transaction signing and broadcasting
- [ ] UniswapX gasless order flow

## Business Side

### Architecture

```
CLIENT (wallet)
  │  signs ERC-3009 authorization off-chain
  ▼
BACKEND  POST /api/pay
  │  relays tx, pays gas
  ▼
PaymentRouter (source chain)
  1. receiveWithAuthorization(from, this, value, ...)
  2. gatewayWallet.depositFor(usdc, shop, amount)
  3. emit PaymentReceived(orderId, from, value)
  │
  ▼
Gateway Wallet ──► Gateway off-chain ledger (unified balance)
  │
  ├──► Watcher
  │      listens PaymentReceived, polls /v1/deposits + /v1/balances
  │
  ├──► Sweep (→ Base)
  │      burn intent → attestation → TreasuryComposer.mintAndDeposit
  │
  └──► Refund (→ any chain)
         burn intent → /v1/transfer?enableForwarder=true → client receives USDC
```

### Project structure

```
gateway/             Gateway client, types, EIP-712 signer, tests
cmd/server/          API server (used by frontend)
cmd/refund/          Admin refund CLI (standalone, same logic as API)
contracts/           Solidity: PaymentRouter, TreasuryComposer, MockVault
relayer/             Relayer backend (POST /api/pay)
watcher/             Payment confirmation watcher
```

Default testnet key: `0x3338A40C3362e6974AA2feCC06a536FF73D6797d` (hardcoded, override with `--private-key` or `PRIVATE_KEY` env).

### API server

Exposes Gateway operations over HTTP for the frontend.

```bash
go run cmd/server/main.go
go run cmd/server/main.go --port 3001  # custom port
```

| Method | Endpoint           | Description                                       |
| ------ | ------------------ | ------------------------------------------------- |
| GET    | `/api/info`        | Gateway domains, contracts, processed heights     |
| GET    | `/api/balances`    | Shop's USDC balances across all domains           |
| POST   | `/api/refund`      | Start a cross-chain refund (returns `transferId`) |
| GET    | `/api/refund/{id}` | Poll refund status until `confirmed`/`finalized`  |

**Manual testing with curl:**

```bash
curl localhost:8080/api/info | jq
curl localhost:8080/api/balances | jq

curl -X POST localhost:8080/api/refund \
  -H "Content-Type: application/json" \
  -d '{"to":"0xCUSTOMER","chain":10,"amount":"1000000"}' | jq

curl localhost:8080/api/refund/TRANSFER_ID | jq
```

### Refund CLI (admin)

Same refund logic as the API, but as a standalone script with polling built-in.

```bash
go run cmd/refund/main.go --to 0xCUSTOMER --chain 10 --amount 1000000
```

### Testing on testnet

#### Prerequisites

Fund the default address `0x3338A40C3362e6974AA2feCC06a536FF73D6797d` with:

1. **Testnet ETH** for gas on both chains:
   - Base Sepolia: https://www.alchemy.com/faucets/base-sepolia
   - Unichain Sepolia: https://www.alchemy.com/faucets/unichain-sepolia

2. **Testnet USDC** on Base Sepolia: https://faucet.circle.com/

3. **RPC URLs** (only needed for on-chain smoke tests):
   ```bash
   export SOURCE_RPC=https://sepolia.base.org
   export DEST_RPC=https://sepolia.unichain.org
   ```

#### Unit tests (no funds needed)

EIP-712 signing, type hashing, address padding, BigInt JSON serialization, balance allocation logic.

```bash
go test ./gateway/ -v
```

#### Gateway API integration tests (no funds needed)

Calls the live Gateway testnet API to verify the HTTP client parses responses correctly: fetches `/v1/info` (chain list, wallet/minter addresses), queries `/v1/balances`, and runs `/v1/estimate` with and without the forwarding service to check fee computation.

```bash
INTEGRATION=1 go test ./gateway/ -run TestSmoke -v
```

#### API server tests (no funds needed)

Tests the HTTP handlers: response format for `/api/info` and `/api/balances`, request validation on `/api/refund` (missing fields, invalid amounts), insufficient balance handling (409), CORS preflight.

```bash
# CORS test only (offline):
go test ./cmd/server/ -run TestCORS -v

# All handler tests (hits live Gateway API):
INTEGRATION=1 go test ./cmd/server/ -v
```

#### Self-managed mint (deposit + transfer + on-chain mint)

Tests the full self-managed flow: deposits USDC into the GatewayWallet on the source chain, waits for the balance to appear in Gateway's off-chain ledger (~20 min), builds and signs a burn intent, calls `/v1/transfer` to get an attestation, then submits `gatewayMint` on the destination chain and verifies the USDC arrived.

```bash
SMOKE=1 go test ./gateway/ -run TestSelfmintFull -v -timeout 35m
SMOKE=1 go test ./gateway/ -run TestSelfmintDeposit -v -timeout 35m  # deposit only
```

#### Forwarding service (transfer without managing the mint yourself)

Tests the forwarding service path: builds and signs a burn intent, submits it with `enableForwarder=true`, then polls `/v1/transfer/{id}` until Circle's forwarder calls `gatewayMint` on the destination chain on our behalf. No destination RPC needed — the forwarder handles the on-chain part.

Requires an existing Gateway balance (run the deposit test above first).

```bash
SMOKE=1 go test ./gateway/ -run TestForwarder -v -timeout 35m
```

#### Env vars

| Variable      | Default   | Description                      |
| ------------- | --------- | -------------------------------- |
| `INTEGRATION` | —         | Enable API-only tests            |
| `SMOKE`       | —         | Enable on-chain smoke tests      |
| `PRIVATE_KEY` | hardcoded | Override the default testnet key |
| `SOURCE_RPC`  | —         | Source chain RPC                 |
| `DEST_RPC`    | —         | Destination chain RPC            |
| `FROM_DOMAIN` | `6`       | Source Gateway domain            |
| `TO_DOMAIN`   | `10`      | Destination Gateway domain       |
| `AMOUNT`      | `1000000` | USDC amount (6 decimals)         |

#### Reference

| Domain | Chain            | USDC                                         |
| ------ | ---------------- | -------------------------------------------- |
| 0      | Ethereum Sepolia | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` |
| 6      | Base Sepolia     | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` |
| 10     | Unichain Sepolia | `0x31d0220469e10c4E71834a79b1f276d740d3768F` |

Gateway contracts (same on all EVM chains): Wallet `0x00777...19B9` / Minter `0x00222...475B`
