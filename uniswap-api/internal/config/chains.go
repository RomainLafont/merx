package config

import "fmt"

// Chain holds network-specific configuration for a supported blockchain.
type Chain struct {
	Name         string
	ChainID      int
	AddressUSDC  string
	AddressWETH  string
	DecimalsUSDC int
}

var (
	EthereumSepolia = &Chain{
		Name:         "ethereum-sepolia",
		ChainID:      11155111,
		AddressUSDC:  "0x94a9d9ac8a22534e3faca9f4e7f2e2cf85d5e4c8",
		AddressWETH:  "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
		DecimalsUSDC: 6,
	}

	BaseSepolia = &Chain{
		Name:         "base-sepolia",
		ChainID:      84532,
		AddressUSDC:  "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		AddressWETH:  "0x4200000000000000000000000000000000000006",
		DecimalsUSDC: 6,
	}

	UnichainSepolia = &Chain{
		Name:         "unichain-sepolia",
		ChainID:      1301,
		AddressUSDC:  "0x31d0220469e10c4e71834a79b1f276d740d3768f",
		AddressWETH:  "0x4200000000000000000000000000000000000006",
		DecimalsUSDC: 6,
	}

	supportedChains = map[string]*Chain{
		"ethereum-sepolia": EthereumSepolia,
		"base-sepolia":     BaseSepolia,
		"unichain-sepolia": UnichainSepolia,
	}
)

// ChainByName returns a chain configuration by its name.
func ChainByName(name string) (*Chain, error) {
	chain, ok := supportedChains[name]
	if !ok {
		return nil, fmt.Errorf("unsupported chain: %s", name)
	}
	return chain, nil
}

// SupportedChainNames returns all supported chain names.
func SupportedChainNames() []string {
	names := make([]string, 0, len(supportedChains))
	for name := range supportedChains {
		names = append(names, name)
	}
	return names
}
