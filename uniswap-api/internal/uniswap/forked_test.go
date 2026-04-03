package uniswap

import (
	"context"
	"testing"

	"merx/internal/config"
)

// loadForkedClient returns a Client configured for mainnet forked tests.
// Skips the test if config.yaml is missing.
func loadForkedClient(t *testing.T) (*Client, *config.Config) {
	t.Helper()
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skipf("config.yaml not found or invalid, skipping forked test: %v", err)
	}
	return NewClient(cfg.BaseURL, cfg.APIKey), cfg
}

// forkedTokenCase describes a single input-token → USDC test vector.
type forkedTokenCase struct {
	Symbol   string
	Address  string
	Amount   string // raw amount in token's smallest unit
	Decimals int
}

var forkedCases = []forkedTokenCase{
	{Symbol: "WETH", Address: config.MainnetWETH, Amount: "100000000000000000", Decimals: 18},   // 0.1 WETH
	{Symbol: "USDT", Address: config.MainnetUSDT, Amount: "100000000", Decimals: 6},              // 100 USDT
	{Symbol: "DAI", Address: config.MainnetDAI, Amount: "100000000000000000000", Decimals: 18},    // 100 DAI
	{Symbol: "WBTC", Address: config.MainnetWBTC, Amount: "1000000", Decimals: 8},                 // 0.01 WBTC
}

// TestForked_QuoteToUSDC verifies that the Uniswap API returns valid quotes
// for swapping each input token into USDC on Ethereum mainnet.
func TestForked_QuoteToUSDC(t *testing.T) {
	client, cfg := loadForkedClient(t)
	chain := config.EthereumMainnet

	for _, tc := range forkedCases {
		t.Run(tc.Symbol+"_to_USDC", func(t *testing.T) {
			resp, err := client.GetPriceInUSDC(
				context.Background(),
				tc.Amount,
				tc.Address,
				chain,
				cfg.SwapperAddress,
			)
			if err != nil {
				t.Fatalf("GetPriceInUSDC(%s → USDC) failed: %v", tc.Symbol, err)
			}

			if resp.Quote.Output.Amount == "" || resp.Quote.Output.Amount == "0" {
				t.Errorf("expected non-zero USDC output for %s, got %q", tc.Symbol, resp.Quote.Output.Amount)
			}
			if resp.Quote.BlockNumber != "" {
				t.Logf("%s: quote at block %s", tc.Symbol, resp.Quote.BlockNumber)
			}
			if resp.Routing == "" {
				t.Errorf("expected non-empty routing for %s", tc.Symbol)
			}

			t.Logf("%s → USDC | block=%s routing=%s input=%s output=%s gasFeeUSD=%s impact=%.4f%%",
				tc.Symbol,
				resp.Quote.BlockNumber,
				resp.Routing,
				resp.Quote.Input.Amount,
				resp.Quote.Output.Amount,
				resp.Quote.GasFeeUSD,
				resp.Quote.PriceImpact,
			)
		})
	}
}

// TestForked_SwapToUSDC verifies that after getting a quote, the Uniswap API
// can produce a valid unsigned swap transaction for each input token into USDC.
func TestForked_SwapToUSDC(t *testing.T) {
	client, cfg := loadForkedClient(t)
	chain := config.EthereumMainnet

	for _, tc := range forkedCases {
		t.Run(tc.Symbol+"_to_USDC", func(t *testing.T) {
			// Step 1: Get quote
			quoteResp, err := client.GetPriceInUSDC(
				context.Background(),
				tc.Amount,
				tc.Address,
				chain,
				cfg.SwapperAddress,
			)
			if err != nil {
				t.Fatalf("GetPriceInUSDC(%s → USDC) failed: %v", tc.Symbol, err)
			}

			// Only proceed with swap for CLASSIC routing (not UniswapX)
			if quoteResp.Routing != "CLASSIC" {
				t.Logf("%s quote returned routing=%s, skipping swap test", tc.Symbol, quoteResp.Routing)
				return
			}

			t.Logf("%s quote: block=%s output=%s USDC",
				tc.Symbol, quoteResp.Quote.BlockNumber, quoteResp.Quote.Output.Amount)

			// Step 2: Build unsigned swap transaction from quote response
			swapResp, err := client.CreateSwap(context.Background(), &SwapRequest{
				Quote: quoteResp.RawQuote,
			})
			if err != nil {
				t.Fatalf("CreateSwap(%s → USDC) failed: %v", tc.Symbol, err)
			}

			if swapResp.Swap.To == "" {
				t.Errorf("expected non-empty 'to' address in swap TX for %s", tc.Symbol)
			}
			if swapResp.Swap.Data == "" {
				t.Errorf("expected non-empty 'data' in swap TX for %s", tc.Symbol)
			}

			t.Logf("%s swap TX: to=%s chainId=%d gasLimit=%s gasFee=%s",
				tc.Symbol,
				swapResp.Swap.To,
				swapResp.Swap.ChainID,
				swapResp.Swap.GasLimit,
				swapResp.GasFee,
			)
		})
	}
}
