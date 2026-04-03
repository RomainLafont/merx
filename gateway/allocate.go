package gateway

import (
	"math/big"
	"sort"
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
		bal, _ := new(big.Int).SetString(b.Balance, 10)
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
