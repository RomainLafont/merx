package gateway

import (
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EIP-712 type strings. Referenced types are appended alphabetically.
const (
	eip712DomainTypeString  = "EIP712Domain(string name,string version)"
	transferSpecTypeString  = "TransferSpec(uint32 version,uint32 sourceDomain,uint32 destinationDomain,bytes32 sourceContract,bytes32 destinationContract,bytes32 sourceToken,bytes32 destinationToken,bytes32 sourceDepositor,bytes32 destinationRecipient,bytes32 sourceSigner,bytes32 destinationCaller,uint256 value,bytes32 salt,bytes hookData)"
	burnIntentTypeString    = "BurnIntent(uint256 maxBlockHeight,uint256 maxFee,TransferSpec spec)" + transferSpecTypeString
	burnIntentSetTypeString = "BurnIntentSet(BurnIntent[] intents)" + burnIntentTypeString
)

// Pre-computed type hashes and domain separator.
var (
	EIP712DomainTypeHash    = crypto.Keccak256Hash([]byte(eip712DomainTypeString))
	TransferSpecTypeHash    = crypto.Keccak256Hash([]byte(transferSpecTypeString))
	BurnIntentTypeHash      = crypto.Keccak256Hash([]byte(burnIntentTypeString))
	BurnIntentSetTypeHash   = crypto.Keccak256Hash([]byte(burnIntentSetTypeString))

	// The Gateway domain uses only name + version (no chainId / verifyingContract).
	// This allows a single signature to authorise burns across any chain.
	domainSeparator = crypto.Keccak256Hash(
		EIP712DomainTypeHash.Bytes(),
		crypto.Keccak256Hash([]byte("GatewayWallet")).Bytes(),
		crypto.Keccak256Hash([]byte("1")).Bytes(),
	)
)

// DomainSeparator returns the constant EIP-712 domain separator used by Gateway.
func DomainSeparator() common.Hash { return domainSeparator }

// ---------------------------------------------------------------------------
// Struct hashing
// ---------------------------------------------------------------------------

// HashTransferSpec computes the EIP-712 struct hash of a TransferSpec.
func HashTransferSpec(spec *TransferSpec) common.Hash {
	var valueBuf common.Hash
	if spec.Value != nil && spec.Value.Int != nil {
		spec.Value.Int.FillBytes(valueBuf[:])
	}

	return crypto.Keccak256Hash(
		TransferSpecTypeHash.Bytes(),
		padUint32(spec.Version),
		padUint32(spec.SourceDomain),
		padUint32(spec.DestinationDomain),
		spec.SourceContract.Bytes(),
		spec.DestinationContract.Bytes(),
		spec.SourceToken.Bytes(),
		spec.DestinationToken.Bytes(),
		spec.SourceDepositor.Bytes(),
		spec.DestinationRecipient.Bytes(),
		spec.SourceSigner.Bytes(),
		spec.DestinationCaller.Bytes(),
		valueBuf.Bytes(),
		spec.Salt.Bytes(),
		crypto.Keccak256Hash(spec.HookData).Bytes(),
	)
}

// HashBurnIntent computes the EIP-712 struct hash of a BurnIntent.
func HashBurnIntent(intent *BurnIntent) common.Hash {
	return crypto.Keccak256Hash(
		BurnIntentTypeHash.Bytes(),
		bigIntToBytes32(intent.MaxBlockHeight),
		bigIntToBytes32(intent.MaxFee),
		HashTransferSpec(&intent.Spec).Bytes(),
	)
}

// HashBurnIntentSet computes the EIP-712 struct hash of a BurnIntentSet.
func HashBurnIntentSet(set *BurnIntentSet) common.Hash {
	// Array hash: keccak256(encodePacked(hash(i0), hash(i1), ...))
	packed := make([]byte, 0, len(set.Intents)*32)
	for i := range set.Intents {
		packed = append(packed, HashBurnIntent(&set.Intents[i]).Bytes()...)
	}
	return crypto.Keccak256Hash(
		BurnIntentSetTypeHash.Bytes(),
		crypto.Keccak256Hash(packed).Bytes(),
	)
}

// ---------------------------------------------------------------------------
// Signing
// ---------------------------------------------------------------------------

// SignBurnIntent signs a single BurnIntent using EIP-712.
func SignBurnIntent(intent *BurnIntent, key *ecdsa.PrivateKey) ([]byte, error) {
	return signEIP712(HashBurnIntent(intent), key)
}

// SignBurnIntentSet signs a BurnIntentSet using EIP-712.
func SignBurnIntentSet(set *BurnIntentSet, key *ecdsa.PrivateKey) ([]byte, error) {
	return signEIP712(HashBurnIntentSet(set), key)
}

func signEIP712(structHash common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	digest := crypto.Keccak256Hash(
		[]byte{0x19, 0x01},
		domainSeparator.Bytes(),
		structHash.Bytes(),
	)

	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		return nil, err
	}
	// go-ethereum returns V as 0/1; EIP-712 expects 27/28.
	sig[64] += 27
	return sig, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func padUint32(v uint32) []byte {
	b := make([]byte, 32)
	big.NewInt(int64(v)).FillBytes(b)
	return b
}

func bigIntToBytes32(b *BigInt) []byte {
	buf := make([]byte, 32)
	if b != nil && b.Int != nil {
		b.Int.FillBytes(buf)
	}
	return buf
}
