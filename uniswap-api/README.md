# uniswap-api

Go client for the [Uniswap Trading API](https://api-docs.uniswap.org/introduction). Get token prices in USDC and execute swaps on supported testnets.

## Supported Chains

| Network | Chain ID |
|---------|----------|
| Ethereum Sepolia | 11155111 |
| Base Sepolia | 84532 |
| Unichain Sepolia | 1301 |

## Setup

1. Get an API key from the [Uniswap Developer Portal](https://developers.uniswap.org/dashboard/)

2. Create your config file:
```bash
cd uniswap-api
cp config.yaml.example config.yaml
# Edit config.yaml with your values
```

```yaml
uniswap_api_key: "your-api-key"
swapper_address: "0xYourWallet"
```

3. Build:
```bash
go build ./uniswap-api/cmd/merx
```

## Usage

### Get a quote (price check)

```bash
go run ./uniswap-api/cmd/merx -amount 100 -chain ethereum-sepolia
```

### Get a quote for a specific token

```bash
go run ./uniswap-api/cmd/merx -amount 50 -token-out 0xfff9976782d46cc05630d1f6ebab18b2324d6b14 -chain ethereum-sepolia
```

### Build an unsigned swap transaction

```bash
go run ./uniswap-api/cmd/merx -amount 100 -chain base-sepolia -swap
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | config.yaml | Path to config file |
| `-amount` | 100.0 | USDC amount to swap |
| `-token-out` | WETH (per chain) | Output token address |
| `-chain` | ethereum-sepolia | Target chain name |
| `-swap` | false | Build unsigned swap TX |

## Testing

### Unit tests (no API key required)

```bash
go test ./uniswap-api/uniswap/ -v
```

### Integration tests (requires config.yaml)

```bash
go test ./uniswap-api/uniswap/ -run Integration -v
```

## Project Structure

```
cmd/merx/main.go       CLI entry point
config/config.go       YAML config loading
config/chains.go       Chain definitions and registry
uniswap/client.go      HTTP client
uniswap/types.go       API request/response types
uniswap/quote.go       GetQuote, GetPriceUSDC
uniswap/swap.go        CreateSwap, CreateOrder
uniswap/approval.go    CheckApproval
```
