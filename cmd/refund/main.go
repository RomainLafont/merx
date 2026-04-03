// Admin refund script: sends USDC to a customer on any supported chain
// via the Gateway Forwarding Service.
//
// Usage:
//
//	PRIVATE_KEY=0x... go run cmd/refund/main.go \
//	  --to 0xCUSTOMER --chain 10 --amount 1000000
//
//	# Force a specific source domain:
//	PRIVATE_KEY=0x... go run cmd/refund/main.go \
//	  --to 0xCUSTOMER --chain 10 --amount 1000000 --from-domain 6
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/RomainLafont/merx/gateway"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Testnet-only key. Address: 0x3338A40C3362e6974AA2feCC06a536FF73D6797d
const defaultPrivateKey = "63de9a8de555c9e160c577087e4d43865f6018aeb5bf919268ed5de5d525a126"
const defaultAddress = "0x3338A40C3362e6974AA2feCC06a536FF73D6797d"

var testnetUSDC = map[uint32]common.Address{
	0:  common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Ethereum Sepolia
	1:  common.HexToAddress("0x5425890298aed601595a70AB815c96711a31Bc65"), // Avalanche Fuji
	6:  common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"), // Base Sepolia
	10: common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F"), // Unichain Sepolia
}

var t0 = time.Now()

func logStep(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Since(t0).Truncate(time.Millisecond), fmt.Sprintf(format, args...))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}

func main() {
	var (
		to         = flag.String("to", "", "recipient address (required)")
		chain      = flag.Uint("chain", 0, "destination domain ID (required)")
		amount     = flag.Int64("amount", 0, "USDC amount in base units (required)")
		fromDomain = flag.Int("from-domain", -1, "force source domain (default: auto-select)")
		privKeyHex = flag.String("private-key", "", "hex private key (or PRIVATE_KEY env)")
	)
	flag.Parse()

	if *to == "" || *chain == 0 || *amount <= 0 {
		fatal("--to, --chain, and --amount are required")
	}

	keyHex := *privKeyHex
	if keyHex == "" {
		keyHex = os.Getenv("PRIVATE_KEY")
	}
	if keyHex == "" {
		keyHex = defaultPrivateKey
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		fatal("invalid private key: %v", err)
	}

	client, err := gateway.NewClient(gateway.Config{PrivateKey: key})
	if err != nil {
		fatal("create client: %v", err)
	}

	ctx := context.Background()
	dst := uint32(*chain)
	recipient := common.HexToAddress(*to)
	amtBig := big.NewInt(*amount)

	logStep("Refund %s USDC → %s on domain %d", amtBig, recipient, dst)
	logStep("Signer: %s", client.SignerAddress())

	// ── /v1/info ──────────────────────────────────────────────────────
	logStep("GET /v1/info")
	info, err := client.GetInfo(ctx)
	if err != nil {
		fatal("GetInfo: %v", err)
	}

	dstDomain := info.LookupDomain(dst)
	if dstDomain == nil {
		fatal("destination domain %d not found in /v1/info", dst)
	}

	// ── /v1/balances ──────────────────────────────────────────────────
	logStep("POST /v1/balances")
	bal, err := client.GetBalances(ctx, &gateway.BalancesRequest{
		Token:   "USDC",
		Sources: []gateway.BalanceSource{{Depositor: client.SignerAddress().Hex()}},
	})
	if err != nil {
		fatal("GetBalances: %v", err)
	}
	for _, b := range bal.Balances {
		logStep("  domain=%d  balance=%s", b.Domain, b.Balance)
	}

	// ── Allocate sources ──────────────────────────────────────────────
	allocs := gateway.AllocateBalances(bal.Balances, amtBig, dst, *fromDomain)
	if allocs == nil {
		fatal("insufficient Gateway balance to cover %s USDC", amtBig)
	}
	logStep("Allocation:")
	for _, a := range allocs {
		logStep("  domain=%d  amount=%s", a.Domain, a.Amount)
	}

	// ── Build burn intents ────────────────────────────────────────────
	logStep("Building %d burn intent(s)", len(allocs))
	var intents []gateway.BurnIntent
	for _, a := range allocs {
		srcDomain := info.LookupDomain(a.Domain)
		if srcDomain == nil {
			fatal("source domain %d not found in /v1/info", a.Domain)
		}

		srcUSDC, ok := testnetUSDC[a.Domain]
		if !ok {
			fatal("no known USDC address for source domain %d", a.Domain)
		}
		dstUSDC, ok := testnetUSDC[dst]
		if !ok {
			fatal("no known USDC address for destination domain %d", dst)
		}

		spec, err := client.BuildTransferSpec(gateway.TransferSpecParams{
			SourceDomain:      a.Domain,
			DestinationDomain: dst,
			SourceWallet:      common.HexToAddress(srcDomain.WalletContract.Address),
			DestinationMinter: common.HexToAddress(dstDomain.MinterContract.Address),
			SourceToken:       srcUSDC,
			DestinationToken:  dstUSDC,
			Depositor:         client.SignerAddress(),
			Recipient:         recipient,
			Value:             a.Amount,
		})
		if err != nil {
			fatal("BuildTransferSpec (domain %d): %v", a.Domain, err)
		}

		intents = append(intents, gateway.BurnIntent{Spec: *spec})
	}

	// ── Estimate ──────────────────────────────────────────────────────
	logStep("POST /v1/estimate?enableForwarder=true")
	est, err := client.Estimate(ctx, intents, &gateway.EstimateOptions{EnableForwarder: true})
	if err != nil {
		fatal("Estimate: %v", err)
	}

	var filled []gateway.BurnIntent
	for i, body := range est.Body {
		bi := body.BurnIntent
		logStep("  intent %d: maxBlockHeight=%s  maxFee=%s", i, bi.MaxBlockHeight, bi.MaxFee)
		filled = append(filled, bi)
	}
	if est.Fees.Total != "" {
		logStep("  fees: total=%s %s", est.Fees.Total, est.Fees.Token)
	}

	// ── Sign ──────────────────────────────────────────────────────────
	logStep("Signing %d burn intent(s)", len(filled))
	var signed []gateway.SignedBurnIntentRequest
	for i := range filled {
		sig, err := client.SignBurnIntent(&filled[i])
		if err != nil {
			fatal("SignBurnIntent %d: %v", i, err)
		}
		signed = append(signed, gateway.SignedBurnIntentRequest{
			BurnIntent: &filled[i],
			Signature:  hexutil.Encode(sig),
		})
	}

	// ── Transfer ──────────────────────────────────────────────────────
	logStep("POST /v1/transfer?enableForwarder=true")
	resp, err := client.Transfer(ctx, signed, &gateway.TransferOptions{EnableForwarder: true})
	if err != nil {
		fatal("Transfer: %v", err)
	}
	logStep("  transferId=%s", resp.TransferID)

	// ── Poll ──────────────────────────────────────────────────────────
	logStep("Polling GET /v1/transfer/%s (interval=15s, timeout=30m)", resp.TransferID)
	status, err := client.PollTransfer(ctx, resp.TransferID, 15*time.Second, 30*time.Minute)
	if err != nil {
		fatal("PollTransfer: %v", err)
	}

	logStep("Refund complete!")
	logStep("  status=%s", status.Status)
	if status.TransactionHash != "" {
		logStep("  txHash=%s", status.TransactionHash)
	}
	logStep("  total time: %s", time.Since(t0).Truncate(time.Second))

	if status.Status == "confirmed" || status.Status == "finalized" {
		fmt.Printf("\n✓ refund of %s USDC to %s on domain %d succeeded\n", amtBig, recipient, dst)
	} else {
		fmt.Printf("\n✗ refund ended with status: %s\n", status.Status)
		os.Exit(1)
	}
}

