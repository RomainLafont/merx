package gateway

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// BigInt wraps *big.Int with decimal-string JSON marshaling (Circle API convention).
type BigInt struct{ *big.Int }

func NewBigInt(x int64) *BigInt              { return &BigInt{big.NewInt(x)} }
func NewBigIntFromBig(x *big.Int) *BigInt    { return &BigInt{Int: new(big.Int).Set(x)} }

func (b BigInt) MarshalJSON() ([]byte, error) {
	if b.Int == nil {
		return json.Marshal("0")
	}
	return json.Marshal(b.Int.String())
}

func (b *BigInt) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		// Also accept a bare JSON number.
		var n json.Number
		if err2 := json.Unmarshal(data, &n); err2 != nil {
			return err
		}
		s = n.String()
	}
	b.Int = new(big.Int)
	if _, ok := b.Int.SetString(s, 10); !ok {
		if _, ok := b.Int.SetString(s, 0); !ok {
			return fmt.Errorf("invalid bigint: %s", s)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Core domain types
// ---------------------------------------------------------------------------

// TransferSpec represents a Gateway transfer specification (14 fields).
type TransferSpec struct {
	Version              uint32        `json:"version"`
	SourceDomain         uint32        `json:"sourceDomain"`
	DestinationDomain    uint32        `json:"destinationDomain"`
	SourceContract       common.Hash   `json:"sourceContract"`
	DestinationContract  common.Hash   `json:"destinationContract"`
	SourceToken          common.Hash   `json:"sourceToken"`
	DestinationToken     common.Hash   `json:"destinationToken"`
	SourceDepositor      common.Hash   `json:"sourceDepositor"`
	DestinationRecipient common.Hash   `json:"destinationRecipient"`
	SourceSigner         common.Hash   `json:"sourceSigner"`
	DestinationCaller    common.Hash   `json:"destinationCaller"`
	Value                *BigInt       `json:"value"`
	Salt                 common.Hash   `json:"salt"`
	HookData             hexutil.Bytes `json:"hookData"`
}

// BurnIntent represents a single burn intent.
type BurnIntent struct {
	MaxBlockHeight *BigInt      `json:"maxBlockHeight,omitempty"`
	MaxFee         *BigInt      `json:"maxFee,omitempty"`
	Spec           TransferSpec `json:"spec"`
}

// BurnIntentSet represents a batch of burn intents (single EIP-712 signature).
type BurnIntentSet struct {
	Intents []BurnIntent `json:"intents"`
}

// ---------------------------------------------------------------------------
// Address ↔ bytes32 helpers
// ---------------------------------------------------------------------------

// AddressToBytes32 left-pads an Ethereum address to 32 bytes.
func AddressToBytes32(addr common.Address) common.Hash {
	var h common.Hash
	copy(h[12:], addr.Bytes())
	return h
}

// Bytes32ToAddress extracts the rightmost 20 bytes as an address.
func Bytes32ToAddress(h common.Hash) common.Address {
	return common.BytesToAddress(h[12:])
}

// ---------------------------------------------------------------------------
// API request / response types
// ---------------------------------------------------------------------------

// GET /v1/info

type InfoResponse struct {
	Version int          `json:"version"`
	Domains []DomainInfo `json:"domains"`
}

// LookupDomain finds a DomainInfo by domain ID.
func (r *InfoResponse) LookupDomain(domain uint32) *DomainInfo {
	for i := range r.Domains {
		if r.Domains[i].Domain == domain {
			return &r.Domains[i]
		}
	}
	return nil
}

type DomainInfo struct {
	Chain                      string       `json:"chain"`
	Network                    string       `json:"network"`
	Domain                     uint32       `json:"domain"`
	WalletContract             ContractInfo `json:"walletContract"`
	MinterContract             ContractInfo `json:"minterContract"`
	ProcessedHeight            string       `json:"processedHeight"`
	BurnIntentExpirationHeight string       `json:"burnIntentExpirationHeight"`
}

type ContractInfo struct {
	Address         string   `json:"address"`
	SupportedTokens []string `json:"supportedTokens"`
}

// POST /v1/balances

type BalancesRequest struct {
	Token   string          `json:"token"`
	Sources []BalanceSource `json:"sources"`
}

type BalanceSource struct {
	Domain    *uint32 `json:"domain,omitempty"`
	Depositor string  `json:"depositor"`
}

type BalancesResponse struct {
	Token    string    `json:"token"`
	Balances []Balance `json:"balances"`
}

type Balance struct {
	Domain    uint32 `json:"domain"`
	Depositor string `json:"depositor"`
	Balance   string `json:"balance"`
}

// POST /v1/deposits

type DepositsRequest struct {
	Token   string          `json:"token"`
	Sources []DepositSource `json:"sources"`
}

type DepositSource struct {
	Domain    uint32 `json:"domain"`
	Depositor string `json:"depositor"`
}

type DepositsResponse struct {
	Token    string    `json:"token"`
	Deposits []Deposit `json:"deposits"`
}

type Deposit struct {
	Depositor       string `json:"depositor"`
	Domain          uint32 `json:"domain"`
	TransactionHash string `json:"transactionHash"`
	Amount          string `json:"amount"`
	Status          string `json:"status"`
	BlockHeight     string `json:"blockHeight"`
	BlockHash       string `json:"blockHash"`
	BlockTimestamp  string `json:"blockTimestamp"`
}

// POST /v1/estimate

type EstimateOptions struct {
	EnableForwarder    bool
	MaxAttestationSize int
}

type EstimateResponse struct {
	Body []EstimateBody `json:"body"`
	Fees Fees           `json:"fees"`
}

type EstimateBody struct {
	BurnIntent BurnIntent `json:"burnIntent"`
}

// POST /v1/transfer

type TransferOptions struct {
	EnableForwarder    bool
	MaxAttestationSize int
}

// SignedBurnIntentRequest is a signed burn intent (or set) sent to POST /v1/transfer.
type SignedBurnIntentRequest struct {
	BurnIntent    *BurnIntent    `json:"burnIntent,omitempty"`
	BurnIntentSet *BurnIntentSet `json:"burnIntentSet,omitempty"`
	Signature     string         `json:"signature"`
}

type TransferResponse struct {
	TransferID      string `json:"transferId"`
	Attestation     string `json:"attestation"`
	Signature       string `json:"signature"`
	Fees            Fees   `json:"fees"`
	ExpirationBlock string `json:"expirationBlock"`
}

// GET /v1/transfer/{id}

type TransferStatusResponse struct {
	DestinationDomain uint32             `json:"destinationDomain"`
	Status            string             `json:"status"`
	BurnIntents       []BurnIntentStatus `json:"burnIntents"`
	TransactionHash   string             `json:"transactionHash,omitempty"`
	ForwardingDetails *ForwardingDetails `json:"forwardingDetails,omitempty"`
	Fees              Fees               `json:"fees"`
	Attestation       *AttestationInfo   `json:"attestation,omitempty"`
}

type BurnIntentStatus struct {
	TransferSpecHash string `json:"transferSpecHash"`
	MaxBlockHeight   string `json:"maxBlockHeight"`
	MaxFee           string `json:"maxFee"`
}

type ForwardingDetails struct {
	ForwardingEnabled bool   `json:"forwardingEnabled"`
	FailureReason     string `json:"failureReason,omitempty"`
}

type AttestationInfo struct {
	Payload         string `json:"payload"`
	Signature       string `json:"signature"`
	ExpirationBlock string `json:"expirationBlock"`
}

// Shared

type Fees struct {
	Total         string      `json:"total"`
	Token         string      `json:"token"`
	PerIntent     []FeeDetail `json:"perIntent"`
	ForwardingFee string      `json:"forwardingFee,omitempty"`
}

type FeeDetail struct {
	TransferSpecHash string `json:"transferSpecHash"`
	Domain           uint32 `json:"domain"`
	BaseFee          string `json:"baseFee"`
	TransferFee      string `json:"transferFee,omitempty"`
}

// ---------------------------------------------------------------------------
// Builder params (user-friendly input for BuildTransferSpec)
// ---------------------------------------------------------------------------

type TransferSpecParams struct {
	SourceDomain      uint32
	DestinationDomain uint32
	SourceWallet      common.Address // GatewayWallet on source chain
	DestinationMinter common.Address // GatewayMinter on destination chain
	SourceToken       common.Address // USDC on source chain
	DestinationToken  common.Address // USDC on destination chain
	Depositor         common.Address // Address being debited
	Recipient         common.Address // Receiver on destination
	DestinationCaller common.Address // Who can call gatewayMint (zero = anyone)
	Value             *big.Int
	HookData          []byte
}
