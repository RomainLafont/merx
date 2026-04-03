package gateway

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// ---------------------------------------------------------------------------
// Test guards
// ---------------------------------------------------------------------------

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run live API tests")
	}
}

func skipUnlessSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("SMOKE") == "" {
		t.Skip("set SMOKE=1 to run on-chain smoke tests")
	}
}

// ---------------------------------------------------------------------------
// Smoke config (env-var driven)
// ---------------------------------------------------------------------------

// Known testnet USDC addresses per Gateway domain.
var testnetUSDC = map[uint32]common.Address{
	0:  common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Ethereum Sepolia
	1:  common.HexToAddress("0x5425890298aed601595a70AB815c96711a31Bc65"), // Avalanche Fuji
	6:  common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"), // Base Sepolia
	10: common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F"), // Unichain Sepolia
}

type smokeConfig struct {
	key       *ecdsa.PrivateKey
	sourceRPC string
	destRPC   string
	from      uint32
	to        uint32
	amount    *big.Int
}

func loadSmokeConfig(t *testing.T) smokeConfig {
	t.Helper()
	skipUnlessSmoke(t)

	keyHex := os.Getenv("PRIVATE_KEY")
	if keyHex == "" {
		t.Fatal("PRIVATE_KEY env required for smoke tests")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		t.Fatalf("invalid PRIVATE_KEY: %v", err)
	}

	from := envUint32(t, "FROM_DOMAIN", 6)
	to := envUint32(t, "TO_DOMAIN", 10)
	amount := envBigInt(t, "AMOUNT", 1_000_000)

	return smokeConfig{
		key:       key,
		sourceRPC: os.Getenv("SOURCE_RPC"),
		destRPC:   os.Getenv("DEST_RPC"),
		from:      from,
		to:        to,
		amount:    amount,
	}
}

func resolveTestnetUSDC(t *testing.T, domain uint32, envKey string) common.Address {
	t.Helper()
	if v := os.Getenv(envKey); v != "" {
		return common.HexToAddress(v)
	}
	addr, ok := testnetUSDC[domain]
	if !ok {
		t.Fatalf("no known USDC address for domain %d — set %s env", domain, envKey)
	}
	return addr
}

func envUint32(t *testing.T, key string, def uint32) uint32 {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		t.Fatalf("invalid %s: %v", key, err)
	}
	return uint32(n)
}

func envBigInt(t *testing.T, key string, def int64) *big.Int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return big.NewInt(def)
	}
	n, ok := new(big.Int).SetString(v, 10)
	if !ok {
		t.Fatalf("invalid %s: %s", key, v)
	}
	return n
}

// ---------------------------------------------------------------------------
// INTEGRATION=1 — API-only tests (no funds, no RPC)
// ---------------------------------------------------------------------------

func TestSmokeGetInfo(t *testing.T) {
	skipUnlessIntegration(t)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}

	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	if info.Version < 1 {
		t.Fatalf("unexpected API version: %d", info.Version)
	}
	if len(info.Domains) == 0 {
		t.Fatal("no domains returned")
	}

	for _, domain := range []uint32{6, 10} {
		d := info.LookupDomain(domain)
		if d == nil {
			t.Fatalf("domain %d not found", domain)
		}
		if d.WalletContract.Address == "" {
			t.Fatalf("domain %d: empty wallet address", domain)
		}
		if d.MinterContract.Address == "" {
			t.Fatalf("domain %d: empty minter address", domain)
		}
		t.Logf("domain=%d chain=%s wallet=%s minter=%s height=%s",
			d.Domain, d.Chain, d.WalletContract.Address, d.MinterContract.Address, d.ProcessedHeight)
	}
}

func TestSmokeGetBalances(t *testing.T) {
	skipUnlessIntegration(t)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}

	depositor := "0x0077777d7EBA4688BDeF3E311b846F25870A19B9"
	bal, err := client.GetBalances(context.Background(), &BalancesRequest{
		Token:   "USDC",
		Sources: []BalanceSource{{Depositor: depositor}},
	})
	if err != nil {
		t.Fatalf("GetBalances: %v", err)
	}

	if len(bal.Balances) == 0 {
		t.Fatal("expected at least one balance entry")
	}

	for _, b := range bal.Balances {
		t.Logf("domain=%d balance=%s", b.Domain, b.Balance)
	}
}

func TestSmokeEstimate(t *testing.T) {
	skipUnlessIntegration(t)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Fetch real wallet/minter addresses from the API.
	info, err := client.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	src := info.LookupDomain(6)
	dst := info.LookupDomain(10)
	if src == nil || dst == nil {
		t.Fatal("domain 6 or 10 not found in /v1/info")
	}

	spec, err := client.BuildTransferSpec(TransferSpecParams{
		SourceDomain:      6,
		DestinationDomain: 10,
		SourceWallet:      common.HexToAddress(src.WalletContract.Address),
		DestinationMinter: common.HexToAddress(dst.MinterContract.Address),
		SourceToken:       testnetUSDC[6],
		DestinationToken:  testnetUSDC[10],
		Depositor:         client.SignerAddress(),
		Recipient:         client.SignerAddress(),
		Value:             big.NewInt(1_000_000),
	})
	if err != nil {
		t.Fatalf("BuildTransferSpec: %v", err)
	}

	intent := BurnIntent{Spec: *spec}

	t.Run("without_forwarder", func(t *testing.T) {
		est, err := client.Estimate(ctx, []BurnIntent{intent}, nil)
		if err != nil {
			t.Fatalf("Estimate: %v", err)
		}
		if len(est.Body) == 0 {
			t.Fatal("empty estimate body")
		}
		filled := est.Body[0].BurnIntent
		if filled.MaxBlockHeight == nil || filled.MaxBlockHeight.Sign() <= 0 {
			t.Fatal("maxBlockHeight should be > 0")
		}
		if filled.MaxFee == nil || filled.MaxFee.Sign() <= 0 {
			t.Fatal("maxFee should be > 0")
		}
		t.Logf("maxBlockHeight=%s maxFee=%s", filled.MaxBlockHeight, filled.MaxFee)
		if est.Fees.Total != "" {
			t.Logf("fees: total=%s %s", est.Fees.Total, est.Fees.Token)
		}
	})

	t.Run("with_forwarder", func(t *testing.T) {
		est, err := client.Estimate(ctx, []BurnIntent{intent}, &EstimateOptions{EnableForwarder: true})
		if err != nil {
			t.Fatalf("Estimate: %v", err)
		}
		if len(est.Body) == 0 {
			t.Fatal("empty estimate body")
		}
		filled := est.Body[0].BurnIntent
		if filled.MaxBlockHeight == nil || filled.MaxBlockHeight.Sign() <= 0 {
			t.Fatal("maxBlockHeight should be > 0")
		}
		if filled.MaxFee == nil || filled.MaxFee.Sign() <= 0 {
			t.Fatal("maxFee should be > 0")
		}
		t.Logf("maxBlockHeight=%s maxFee=%s", filled.MaxBlockHeight, filled.MaxFee)
		if est.Fees.Total != "" {
			t.Logf("fees: total=%s %s (forwardingFee=%s)", est.Fees.Total, est.Fees.Token, est.Fees.ForwardingFee)
		}
	})
}

func TestSmokeAddressPadding(t *testing.T) {
	skipUnlessIntegration(t)

	addr := common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e")
	b32 := AddressToBytes32(addr)
	recovered := Bytes32ToAddress(b32)

	if recovered != addr {
		t.Fatalf("roundtrip mismatch: got %s, want %s", recovered, addr)
	}
	t.Logf("bytes32: %#x", b32)
}
