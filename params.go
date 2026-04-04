// Testnet addresses and configuration shared across commands.
package merx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Shop wallet (backend signer). Override with PRIVATE_KEY env or --private-key flag.
const DefaultPrivateKey = "4c5c7916326aa54e80c39792003ac7d9464b0fb0558678fc16040c9d9322aa41"

// ---------------------------------------------------------------------------
// CCTP domains & chain IDs
// ---------------------------------------------------------------------------

// ChainIDToDomain maps EVM chain IDs to CCTP domain IDs.
var ChainIDToDomain = map[uint64]uint32{
	84532: 6,  // Base Sepolia
	1301:  10, // Unichain Sepolia
}

// ---------------------------------------------------------------------------
// USDC addresses (testnet)
// ---------------------------------------------------------------------------

var TestnetUSDC = map[uint32]common.Address{
	6:  common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"), // Base Sepolia
	10: common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F"), // Unichain Sepolia
	26: common.HexToAddress("0x3600000000000000000000000000000000000000"), // Arc Testnet
}

// ---------------------------------------------------------------------------
// Deployed contracts
// ---------------------------------------------------------------------------

// ShopPaymaster addresses by EVM chain ID (one per source chain).
var ShopPaymaster = map[uint64]common.Address{
	1301: common.HexToAddress("0xb0262c0Cb99329706126Cae0f152C575067e450a"), // Unichain Sepolia
}

// ArcReceiver on Arc testnet — self-relays CCTP messages and deposits into Gateway.
var ArcReceiver = common.HexToAddress("0x0A4eFeFbB7286D864cDDf6957642b2B11cd58f30")

// ---------------------------------------------------------------------------
// RPC endpoints
// ---------------------------------------------------------------------------

var RPCURLs = map[uint64]string{
	1301:  "https://sepolia.unichain.org",
	84532: "https://sepolia.base.org",
}

const ArcRPCURL = "https://rpc.testnet.arc.network"
const ArcChainID = 5042002

// ---------------------------------------------------------------------------
// CCTP API
// ---------------------------------------------------------------------------

const CCTPAttestationURL = "https://iris-api-sandbox.circle.com/v2/messages"

// DefaultMaxFee for CCTP V2 Fast Transfer (USDC, 6 decimals).
// Unichain: 1.5 bps. For 100 USDC = $0.015. $0.05 with margin.
var DefaultMaxFee = big.NewInt(50_000)
