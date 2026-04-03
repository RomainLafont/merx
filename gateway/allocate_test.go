package gateway

import (
	"math/big"
	"testing"
)

func TestAllocateBalances_SingleSource(t *testing.T) {
	balances := []Balance{
		{Domain: 6, Balance: "5000000"},
		{Domain: 10, Balance: "2000000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(3000000), 10, -1)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Domain != 6 {
		t.Fatalf("expected domain 6, got %d", allocs[0].Domain)
	}
	if allocs[0].Amount.Cmp(big.NewInt(3000000)) != 0 {
		t.Fatalf("expected 3000000, got %s", allocs[0].Amount)
	}
}

func TestAllocateBalances_MultiSource(t *testing.T) {
	balances := []Balance{
		{Domain: 0, Balance: "1000000"},
		{Domain: 6, Balance: "2000000"},
		{Domain: 3, Balance: "500000"},
	}
	// Need 3000000, no single domain covers it. Dest=10 (excluded anyway since not in list).
	allocs := AllocateBalances(balances, big.NewInt(3000000), 10, -1)
	if len(allocs) != 2 {
		t.Fatalf("expected 2 allocations, got %d", len(allocs))
	}

	total := new(big.Int)
	for _, a := range allocs {
		total.Add(total, a.Amount)
	}
	if total.Cmp(big.NewInt(3000000)) != 0 {
		t.Fatalf("total allocated %s, want 3000000", total)
	}
}

func TestAllocateBalances_ExcludesDestination(t *testing.T) {
	balances := []Balance{
		{Domain: 6, Balance: "5000000"},
		{Domain: 10, Balance: "9000000"},
	}
	// Dest=10, so domain 10 is excluded. Only domain 6 available.
	allocs := AllocateBalances(balances, big.NewInt(3000000), 10, -1)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Domain != 6 {
		t.Fatalf("expected domain 6, got %d", allocs[0].Domain)
	}
}

func TestAllocateBalances_ForceDomain(t *testing.T) {
	balances := []Balance{
		{Domain: 0, Balance: "9000000"},
		{Domain: 6, Balance: "2000000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(1000000), 10, 6)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Domain != 6 {
		t.Fatalf("expected forced domain 6, got %d", allocs[0].Domain)
	}
}

func TestAllocateBalances_Insufficient(t *testing.T) {
	balances := []Balance{
		{Domain: 6, Balance: "500000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(1000000), 10, -1)
	if allocs != nil {
		t.Fatalf("expected nil for insufficient balance, got %v", allocs)
	}
}

func TestAllocateBalances_PicksLargestFirst(t *testing.T) {
	balances := []Balance{
		{Domain: 0, Balance: "100000"},
		{Domain: 6, Balance: "3000000"},
		{Domain: 3, Balance: "500000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(2000000), 10, -1)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Domain != 6 {
		t.Fatalf("expected largest domain 6 first, got %d", allocs[0].Domain)
	}
}

func TestAllocateBalances_TakesOnlyNeeded(t *testing.T) {
	balances := []Balance{
		{Domain: 6, Balance: "5000000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(1000000), 10, -1)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Amount.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("should only take needed amount, got %s", allocs[0].Amount)
	}
}

func TestAllocateBalances_SkipsZeroBalances(t *testing.T) {
	balances := []Balance{
		{Domain: 0, Balance: "0"},
		{Domain: 6, Balance: "2000000"},
	}
	allocs := AllocateBalances(balances, big.NewInt(1000000), 10, -1)
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(allocs))
	}
	if allocs[0].Domain != 6 {
		t.Fatalf("expected domain 6 (skipping zero), got %d", allocs[0].Domain)
	}
}
