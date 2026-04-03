package gateway

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// TestForwarderTransfer runs the full forwarding service smoke test.
//
// Requires: SMOKE=1  PRIVATE_KEY=0x...
// Optional: FROM_DOMAIN (default 6), TO_DOMAIN (default 10), AMOUNT (default 1000000)
//           SOURCE_USDC, DEST_USDC (override known testnet addresses)
//
//	SMOKE=1 PRIVATE_KEY=0x... go test ./gateway/ -run TestForwarder -v -timeout 35m
func TestForwarderTransfer(t *testing.T) {
	cfg := loadSmokeConfig(t)

	client, err := NewClient(Config{PrivateKey: cfg.key})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	signer := client.SignerAddress()
	t.Logf("signer: %s", signer)

	// --- /v1/info ---
	info, err := client.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	srcDomain := info.LookupDomain(cfg.from)
	dstDomain := info.LookupDomain(cfg.to)
	if srcDomain == nil {
		t.Fatalf("source domain %d not found", cfg.from)
	}
	if dstDomain == nil {
		t.Fatalf("destination domain %d not found", cfg.to)
	}

	// --- /v1/balances ---
	bal, err := client.GetBalances(ctx, &BalancesRequest{
		Token:   "USDC",
		Sources: []BalanceSource{{Depositor: signer.Hex()}},
	})
	if err != nil {
		t.Fatalf("GetBalances: %v", err)
	}

	var found bool
	for _, b := range bal.Balances {
		t.Logf("domain=%d balance=%s", b.Domain, b.Balance)
		if b.Domain == cfg.from {
			balance, _ := new(big.Int).SetString(b.Balance, 10)
			if balance != nil && balance.Cmp(cfg.amount) >= 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("insufficient Gateway balance on domain %d (need %s). Deposit USDC first.", cfg.from, cfg.amount)
	}

	// --- Build TransferSpec ---
	srcUSDC := resolveTestnetUSDC(t, cfg.from, "SOURCE_USDC")
	dstUSDC := resolveTestnetUSDC(t, cfg.to, "DEST_USDC")

	spec, err := client.BuildTransferSpec(TransferSpecParams{
		SourceDomain:      cfg.from,
		DestinationDomain: cfg.to,
		SourceWallet:      common.HexToAddress(srcDomain.WalletContract.Address),
		DestinationMinter: common.HexToAddress(dstDomain.MinterContract.Address),
		SourceToken:       srcUSDC,
		DestinationToken:  dstUSDC,
		Depositor:         signer,
		Recipient:         signer,
		Value:             cfg.amount,
	})
	if err != nil {
		t.Fatalf("BuildTransferSpec: %v", err)
	}

	// --- Estimate ---
	intent := BurnIntent{Spec: *spec}
	est, err := client.Estimate(ctx, []BurnIntent{intent}, &EstimateOptions{EnableForwarder: true})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}

	filled := est.Body[0].BurnIntent
	t.Logf("estimate: maxBlockHeight=%s maxFee=%s", filled.MaxBlockHeight, filled.MaxFee)

	// --- Sign ---
	sig, err := client.SignBurnIntent(&filled)
	if err != nil {
		t.Fatalf("SignBurnIntent: %v", err)
	}
	t.Logf("signature: %s", hexutil.Encode(sig))

	// --- Transfer ---
	resp, err := client.Transfer(ctx, []SignedBurnIntentRequest{{
		BurnIntent: &filled,
		Signature:  hexutil.Encode(sig),
	}}, &TransferOptions{EnableForwarder: true})
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	t.Logf("transferId: %s", resp.TransferID)

	// --- Poll ---
	t.Logf("polling (interval=15s, timeout=30m)...")
	status, err := client.PollTransfer(ctx, resp.TransferID, 15*time.Second, 30*time.Minute)
	if err != nil {
		t.Fatalf("PollTransfer: %v", err)
	}

	t.Logf("status=%s txHash=%s", status.Status, status.TransactionHash)
	if status.ForwardingDetails != nil {
		t.Logf("forwarding=%v failureReason=%s", status.ForwardingDetails.ForwardingEnabled, status.ForwardingDetails.FailureReason)
	}

	if status.Status != "confirmed" && status.Status != "finalized" {
		t.Fatalf("unexpected terminal status: %s", status.Status)
	}
}
