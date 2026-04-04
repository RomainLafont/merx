package gateway

import (
	"math/big"
	"sort"
	"strings"
)

// Allocation represents USDC to draw from a single source domain.
type Allocation struct {
	Domain uint32
	Amount *big.Int
}

// AllocateBalances picks source domain(s) to cover the target amount.
// It excludes excludeDomain (typically the destination) and, if forceDomain >= 0,
// restricts to that single domain. Returns nil if insufficient balance.
func AllocateBalances(balances []Balance, target *big.Int, excludeDomain uint32, forceDomain int) []Allocation {
	type entry struct {
		domain  uint32
		balance *big.Int
	}

	var candidates []entry
	for _, b := range balances {
		if b.Domain == excludeDomain {
			continue
		}
		if forceDomain >= 0 && b.Domain != uint32(forceDomain) {
			continue
		}
		bal := parseUSDCBalance(b.Balance)
		if bal != nil && bal.Sign() > 0 {
			candidates = append(candidates, entry{domain: b.Domain, balance: bal})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].balance.Cmp(candidates[j].balance) > 0
	})

	remaining := new(big.Int).Set(target)
	var result []Allocation

	for _, c := range candidates {
		if remaining.Sign() <= 0 {
			break
		}
		take := new(big.Int).Set(c.balance)
		if take.Cmp(remaining) > 0 {
			take.Set(remaining)
		}
		result = append(result, Allocation{Domain: c.domain, Amount: take})
		remaining.Sub(remaining, take)
	}

	if remaining.Sign() > 0 {
		return nil
	}
	return result
}

// parseUSDCBalance parses a balance string that may be either an integer ("1000000")
// or a decimal ("2.499700") and returns the value in base units (6 decimals).
func parseUSDCBalance(s string) *big.Int {
	// Try integer first.
	if v, ok := new(big.Int).SetString(s, 10); ok {
		return v
	}
	// Parse decimal: "2.499700" → 2499700
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return nil
	}
	whole, ok := new(big.Int).SetString(parts[0], 10)
	if !ok {
		return nil
	}
	frac := parts[1]
	// Pad or truncate to 6 decimal places.
	for len(frac) < 6 {
		frac += "0"
	}
	frac = frac[:6]
	fracInt, ok := new(big.Int).SetString(frac, 10)
	if !ok {
		return nil
	}
	// whole * 1_000_000 + frac
	whole.Mul(whole, big.NewInt(1_000_000))
	whole.Add(whole, fracInt)
	return whole
}
