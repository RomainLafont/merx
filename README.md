# Merx

On-chain shop POC built on Circle Gateway. A customer pays in USDC from any supported chain — gasless for the customer — the shop accumulates a unified Gateway balance on Arc, and can refund cross-chain instantly.

## Architecture

### Payment flow (customer → shop)

```
Customer (Unichain / Base)
  │
  │ signs EIP-2612 permit off-chain (gasless)
  ▼
Backend  POST /api/pay
  │ broadcasts payWithPermit tx (pays gas)
  ▼
ShopPaymaster (source chain)
  │ permit → transferFrom → approve → depositForBurn
  ▼
CCTP V2 Fast Transfer
  │ burn USDC on source, attestation in ~5s
  ▼
Backend (self-relay)
  │ polls attestation API, then calls relayAndDeposit
  ▼
ArcReceiver (Arc)
  │ receiveMessage (mint USDC) → depositFor on Gateway
  ▼
Gateway off-chain ledger (unified balance on Arc)
```

### Refund flow (shop → customer)

```
Backend  POST /api/refund
  │ read Gateway balances, allocate sources
  │ build + sign burn intent(s)
  │ POST /v1/transfer?enableForwarder=true
  ▼
Gateway Forwarding Service
  │ burns from Gateway balance, mints on destination
  ▼
Customer receives USDC on target chain
```

## Project structure

```
params.go                Shared addresses, chain config, RPC URLs
gateway/                 Gateway client, types, EIP-712 signer, tests
cmd/server/              API server (pay-tx, pay, refund, info, balances)
cmd/refund/              Admin refund CLI
contracts/src/           Solidity: ShopPaymaster, ArcReceiver
contracts/test/          Foundry tests
contracts/script/        Deploy scripts
```

## Wallets

| Role | Address | Chains |
|------|---------|--------|
| Shop (backend) | `0x2A94238046B648EFF3Ec899fbe6C2B7990C52ca3` | Unichain Sepolia, Arc |
| Customer (test) | `0xF761522Bb65aE69d1DF264Ce3D48e43E84E98D97` | Unichain Sepolia, Arc |

Override with `--private-key` or `PRIVATE_KEY` env.

## Deployed contracts

| Contract | Chain | Address |
|----------|-------|---------|
| ShopPaymaster | Unichain Sepolia | `0xb0262c0Cb99329706126Cae0f152C575067e450a` |
| ArcReceiver | Arc Testnet | `0x0A4eFeFbB7286D864cDDf6957642b2B11cd58f30` |

## API server

```bash
go run cmd/server/main.go
go run cmd/server/main.go --port 3001
```

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/info` | Gateway domains, contracts, processed heights |
| GET | `/api/balances` | Shop's USDC balances across all domains |
| GET | `/api/pay-tx` | Get permit data for customer to sign (off-chain, gasless) |
| POST | `/api/pay` | Submit signed permit — backend broadcasts + self-relays to Arc |
| POST | `/api/refund` | Start a cross-chain refund (returns `transferId`) |
| GET | `/api/refund/{id}` | Poll refund status until `confirmed`/`finalized` |

### Payment example

```bash
# 1. Get permit data
curl "localhost:8080/api/pay-tx?chain_id=1301&amount=1000000" | jq

# 2. Customer signs the EIP-2612 permit off-chain (gasless)

# 3. Submit signed permit — backend pays gas, bridges to Arc, deposits into Gateway
curl -X POST localhost:8080/api/pay \
  -H "Content-Type: application/json" \
  -d '{"owner":"0xCUSTOMER","chain_id":1301,"amount":"1000000","deadline":"...","signature":"0x..."}' | jq
```

### Refund example

```bash
curl -X POST localhost:8080/api/refund \
  -H "Content-Type: application/json" \
  -d '{"to":"0xCUSTOMER","chain":10,"amount":"1000000"}' | jq

curl localhost:8080/api/refund/TRANSFER_ID | jq
```

## Testing

### Unit tests (offline)

```bash
go test ./gateway/ -v
go test ./cmd/server/ -run TestCORS -v
```

### Foundry tests (offline)

```bash
cd contracts && forge test -vvv
```

### Integration tests (hits live Gateway API, no funds needed)

```bash
INTEGRATION=1 go test ./gateway/ -run TestSmoke -v
INTEGRATION=1 go test ./cmd/server/ -v
```

### On-chain smoke tests (requires funded wallets)

```bash
SMOKE=1 go test ./gateway/ -run TestSelfmintFull -v -timeout 35m
SMOKE=1 go test ./gateway/ -run TestForwarder -v -timeout 35m
```

## Reference

| Domain | Chain | USDC | Chain ID |
|--------|-------|------|----------|
| 6 | Base Sepolia | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | 84532 |
| 10 | Unichain Sepolia | `0x31d0220469e10c4E71834a79b1f276d740d3768F` | 1301 |
| 26 | Arc Testnet | `0x3600000000000000000000000000000000000000` | 5042002 |

Gateway Wallet (all chains): `0x0077777d7EBA4688BDeF3E311b846F25870A19B9`

TokenMessengerV2 (all testnets): `0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA`

MessageTransmitter (Arc): `0xE737e5cEBEEBa77EFE34D4aa090756590b1CE275`
