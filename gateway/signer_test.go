package gateway

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// ---------------------------------------------------------------------------
// Address ↔ bytes32
// ---------------------------------------------------------------------------

func TestAddressToBytes32Roundtrip(t *testing.T) {
	addr := common.HexToAddress("0xdead00000000000000000000000000000000beef")
	b32 := AddressToBytes32(addr)

	// First 12 bytes must be zero.
	for i := 0; i < 12; i++ {
		if b32[i] != 0 {
			t.Fatalf("byte %d should be 0, got %02x", i, b32[i])
		}
	}

	recovered := Bytes32ToAddress(b32)
	if recovered != addr {
		t.Fatalf("roundtrip failed: got %s, want %s", recovered, addr)
	}
}

// ---------------------------------------------------------------------------
// EIP-712 domain type hash
// ---------------------------------------------------------------------------

func TestEIP712DomainTypeHash(t *testing.T) {
	// Known constant from circlefin/evm-gateway-contracts EIP712Domain.sol.
	want := common.HexToHash("0xb03948446334eb9b2196d5eb166f69b9d49403eb4a12f36de8d3f9f3cb8e15c3")
	if EIP712DomainTypeHash != want {
		t.Fatalf("domain type hash mismatch:\n  got  %s\n  want %s", EIP712DomainTypeHash, want)
	}
}

// ---------------------------------------------------------------------------
// Domain separator (constant — name + version only)
// ---------------------------------------------------------------------------

func TestDomainSeparator(t *testing.T) {
	// Manually compute: keccak256(abi.encode(domainTypeHash, keccak256("GatewayWallet"), keccak256("1")))
	nameHash := crypto.Keccak256Hash([]byte("GatewayWallet"))
	versionHash := crypto.Keccak256Hash([]byte("1"))
	want := crypto.Keccak256Hash(
		EIP712DomainTypeHash.Bytes(),
		nameHash.Bytes(),
		versionHash.Bytes(),
	)
	got := DomainSeparator()
	if got != want {
		t.Fatalf("domain separator mismatch:\n  got  %s\n  want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// TransferSpec hashing
// ---------------------------------------------------------------------------

func TestHashTransferSpec_Deterministic(t *testing.T) {
	spec := makeTestSpec()
	h1 := HashTransferSpec(spec)
	h2 := HashTransferSpec(spec)
	if h1 != h2 {
		t.Fatal("same input produced different hashes")
	}
}

func TestHashTransferSpec_ChangesOnFieldChange(t *testing.T) {
	spec := makeTestSpec()
	h1 := HashTransferSpec(spec)

	spec.Value = NewBigInt(999)
	h2 := HashTransferSpec(spec)

	if h1 == h2 {
		t.Fatal("changing value should change the hash")
	}
}

// ---------------------------------------------------------------------------
// BurnIntent hashing
// ---------------------------------------------------------------------------

func TestHashBurnIntent_IncludesSpec(t *testing.T) {
	intent := makeTestIntent()
	h1 := HashBurnIntent(intent)

	intent.Spec.Value = NewBigInt(42)
	h2 := HashBurnIntent(intent)

	if h1 == h2 {
		t.Fatal("changing spec should change the burn intent hash")
	}
}

// ---------------------------------------------------------------------------
// BurnIntentSet hashing
// ---------------------------------------------------------------------------

func TestHashBurnIntentSet_OrderMatters(t *testing.T) {
	i1 := makeTestIntent()
	i2 := makeTestIntent()
	i2.MaxFee = NewBigInt(999)

	set1 := &BurnIntentSet{Intents: []BurnIntent{*i1, *i2}}
	set2 := &BurnIntentSet{Intents: []BurnIntent{*i2, *i1}}

	if HashBurnIntentSet(set1) == HashBurnIntentSet(set2) {
		t.Fatal("different order should produce different hashes")
	}
}

// ---------------------------------------------------------------------------
// Signing & recovery
// ---------------------------------------------------------------------------

func TestSignBurnIntent_Recoverable(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	expected := crypto.PubkeyToAddress(key.PublicKey)

	intent := makeTestIntent()
	sig, err := SignBurnIntent(intent, key)
	if err != nil {
		t.Fatal(err)
	}

	if len(sig) != 65 {
		t.Fatalf("signature length: got %d, want 65", len(sig))
	}
	if sig[64] != 27 && sig[64] != 28 {
		t.Fatalf("V byte: got %d, want 27 or 28", sig[64])
	}

	// Recover the signer.
	structHash := HashBurnIntent(intent)
	digest := crypto.Keccak256Hash(
		[]byte{0x19, 0x01},
		DomainSeparator().Bytes(),
		structHash.Bytes(),
	)
	recoverSig := make([]byte, 65)
	copy(recoverSig, sig)
	recoverSig[64] -= 27 // back to 0/1 for ecrecover

	pub, err := crypto.Ecrecover(digest.Bytes(), recoverSig)
	if err != nil {
		t.Fatal(err)
	}
	recovered := common.BytesToAddress(crypto.Keccak256(pub[1:])[12:])
	if recovered != expected {
		t.Fatalf("recovered %s, want %s", recovered, expected)
	}
}

func TestSignBurnIntentSet_Recoverable(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	expected := crypto.PubkeyToAddress(key.PublicKey)

	set := &BurnIntentSet{Intents: []BurnIntent{*makeTestIntent()}}
	sig, err := SignBurnIntentSet(set, key)
	if err != nil {
		t.Fatal(err)
	}

	structHash := HashBurnIntentSet(set)
	digest := crypto.Keccak256Hash(
		[]byte{0x19, 0x01},
		DomainSeparator().Bytes(),
		structHash.Bytes(),
	)
	recoverSig := make([]byte, 65)
	copy(recoverSig, sig)
	recoverSig[64] -= 27

	pub, err := crypto.Ecrecover(digest.Bytes(), recoverSig)
	if err != nil {
		t.Fatal(err)
	}
	recovered := common.BytesToAddress(crypto.Keccak256(pub[1:])[12:])
	if recovered != expected {
		t.Fatalf("recovered %s, want %s", recovered, expected)
	}
}

// ---------------------------------------------------------------------------
// BigInt JSON
// ---------------------------------------------------------------------------

func TestBigIntJSON(t *testing.T) {
	tests := []struct {
		name string
		val  *BigInt
		json string
	}{
		{"zero", NewBigInt(0), `"0"`},
		{"positive", NewBigInt(1000000), `"1000000"`},
		{"large", NewBigIntFromBig(new(big.Int).Exp(big.NewInt(2), big.NewInt(128), nil)), `"340282366920938463463374607431768211456"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.val)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.json {
				t.Fatalf("marshal: got %s, want %s", data, tt.json)
			}

			var got BigInt
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Int.Cmp(tt.val.Int) != 0 {
				t.Fatalf("unmarshal: got %s, want %s", got.Int, tt.val.Int)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeTestSpec() *TransferSpec {
	return &TransferSpec{
		Version:              1,
		SourceDomain:         6,
		DestinationDomain:    10,
		SourceContract:       AddressToBytes32(common.HexToAddress("0x0077777d7EBA4688BDeF3E311b846F25870A19B9")),
		DestinationContract:  AddressToBytes32(common.HexToAddress("0x0022222ABE238Cc2C7Bb1f21003F0a260052475B")),
		SourceToken:          AddressToBytes32(common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e")),
		DestinationToken:     AddressToBytes32(common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F")),
		SourceDepositor:      AddressToBytes32(common.HexToAddress("0x1111111111111111111111111111111111111111")),
		DestinationRecipient: AddressToBytes32(common.HexToAddress("0x2222222222222222222222222222222222222222")),
		SourceSigner:         AddressToBytes32(common.HexToAddress("0x3333333333333333333333333333333333333333")),
		DestinationCaller:    common.Hash{}, // anyone
		Value:                NewBigInt(1_000_000),
		Salt:                 common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
		HookData:             []byte{},
	}
}

func makeTestIntent() *BurnIntent {
	return &BurnIntent{
		MaxBlockHeight: NewBigInt(99999999),
		MaxFee:         NewBigInt(2_010_000),
		Spec:           *makeTestSpec(),
	}
}
