// Testnet addresses and configuration shared across commands.
package merx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Shop wallet (backend signer). Override with PRIVATE_KEY env or --private-key flag.
const DefaultPrivateKey = "4c5c7916326aa54e80c39792003ac7d9464b0fb0558678fc16040c9d9322aa41"

// Shop wallet address: 0x2A94238046B648EFF3Ec899fbe6C2B7990C52ca3

// ---------------------------------------------------------------------------
// CCTP V2 contracts (same on all testnets)
// ---------------------------------------------------------------------------

var TokenMessengerV2 = common.HexToAddress("0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA")
var MessageTransmitter = common.HexToAddress("0xE737e5cEBEEBa77EFE34D4aa090756590b1CE275")

// ForwardingHookData is the 32-byte hookData for CCTP Forwarding Service:
// "cctp-forward" magic (24 bytes) + version 0 (4 bytes) + data length 0 (4 bytes).
var ForwardingHookData = common.FromHex("636374702d666f72776172640000000000000000000000000000000000000000")

// USDC addresses by CCTP domain (testnet).
var TestnetUSDC = map[uint32]common.Address{
	0:  common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Ethereum Sepolia
	6:  common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"), // Base Sepolia
	10: common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F"), // Unichain Sepolia
	26: common.HexToAddress("0x3600000000000000000000000000000000000000"), // Arc Testnet
}

// ---------------------------------------------------------------------------
// Deployed contracts
// ---------------------------------------------------------------------------

// CompoundDepositor on Ethereum Sepolia — CCTP receiveMessage + supply into Compound V3.
var CompoundDepositor = common.HexToAddress("0x832705f381957C8218d7ae8B20A10d510B5AFB75")

// ---------------------------------------------------------------------------
// RPC endpoints
// ---------------------------------------------------------------------------

var RPCURLs = map[uint64]string{
	11155111: "https://ethereum-sepolia-rpc.publicnode.com",
	1301:     "https://sepolia.unichain.org",
	84532:    "https://sepolia.base.org",
}

const ArcRPCURL = "https://rpc.testnet.arc.network"
const ArcChainID int64 = 5042002
const ArcDomain uint32 = 26

// ---------------------------------------------------------------------------
// CCTP API
// ---------------------------------------------------------------------------

const CCTPAttestationURL = "https://iris-api-sandbox.circle.com/v2/messages"

// ForwardingMaxFee for CCTP Forwarding Service (USDC, 6 decimals).
// The Forwarding Service takes the entire maxFee (not just the minimum).
// $0.50 is the tested working value on testnets.
var ForwardingMaxFee = big.NewInt(500_000)

// DefaultMaxFee for CCTP V2 Fast Transfer without forwarding (USDC, 6 decimals).
var DefaultMaxFee = big.NewInt(50_000)

// ---------------------------------------------------------------------------
// Compound V3 (Ethereum Sepolia)
// ---------------------------------------------------------------------------

var CompoundComet = common.HexToAddress("0xAec1F48e02Cfb822Be958B68C7957156EB3F0b6e")
