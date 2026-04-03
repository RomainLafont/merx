package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"merx/internal/config"
	"merx/internal/uniswap"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "path to config.yaml")
	amount := flag.Float64("amount", 100.0, "USDC amount to swap")
	tokenOut := flag.String("token-out", "", "output token address (default: WETH on selected chain)")
	chainName := flag.String("chain", "ethereum-sepolia", "chain name: "+strings.Join(config.SupportedChainNames(), ", "))
	doSwap := flag.Bool("swap", false, "build unsigned swap transaction (default: quote only)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	chain, err := config.ChainByName(*chainName)
	if err != nil {
		log.Fatalf("Chain error: %v", err)
	}

	if *tokenOut == "" {
		*tokenOut = chain.AddressWETH
	}

	ctx := context.Background()
	client := uniswap.NewClient(cfg.BaseURL, cfg.APIKey)

	fmt.Printf("Getting quote: %.2f USDC -> %s on %s...\n", *amount, *tokenOut, chain.Name)
	quoteResp, err := client.GetPriceUSDC(ctx, *amount, *tokenOut, chain, cfg.SwapperAddress)
	if err != nil {
		log.Fatalf("Quote error: %v", err)
	}

	fmt.Printf("Routing:      %s\n", quoteResp.Routing)
	fmt.Printf("Input:        %s (token: %s)\n", quoteResp.Quote.Input.Amount, quoteResp.Quote.Input.Token)
	fmt.Printf("Output:       %s (token: %s)\n", quoteResp.Quote.Output.Amount, quoteResp.Quote.Output.Token)
	fmt.Printf("Gas fee:      %s USD\n", quoteResp.Quote.GasFeeUSD)
	fmt.Printf("Price impact: %.4f%%\n", quoteResp.Quote.PriceImpact)

	if !*doSwap {
		os.Exit(0)
	}

	fmt.Println("\nChecking token approval...")
	approvalResp, err := client.CheckApproval(ctx, &uniswap.ApprovalRequest{
		WalletAddress: cfg.SwapperAddress,
		Token:         chain.AddressUSDC,
		Amount:        quoteResp.Quote.Input.Amount,
		ChainID:       chain.ChainID,
	})
	if err != nil {
		log.Fatalf("Approval error: %v", err)
	}
	if approvalResp.Approval != nil {
		fmt.Printf("Approval TX needed: to=%s\n", approvalResp.Approval.To)
	} else {
		fmt.Println("Token already approved.")
	}

	fmt.Println("\nBuilding swap transaction...")
	swapResp, err := client.CreateSwap(ctx, &uniswap.SwapRequest{
		Quote: quoteResp.Quote,
	})
	if err != nil {
		log.Fatalf("Swap error: %v", err)
	}

	fmt.Printf("Swap TX: to=%s, value=%s, chainId=%d\n", swapResp.Swap.To, swapResp.Swap.Value, swapResp.Swap.ChainID)
	fmt.Printf("Gas fee: %s wei\n", swapResp.GasFee)
	fmt.Println("\nTransaction is unsigned. Sign and broadcast with your wallet to execute.")
}
