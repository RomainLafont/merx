package gateway

import (
	"context"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run live API tests")
	}
}

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

	// Verify Base Sepolia (domain 6) and Unichain Sepolia (domain 10) are present.
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

	// Query the GatewayWallet address — will return zero balances but validates the roundtrip.
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
