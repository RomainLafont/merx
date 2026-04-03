package gateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	TestnetBaseURL    = "https://gateway-api-testnet.circle.com"
	ProductionBaseURL = "https://gateway-api.circle.com"

	DefaultPollInterval = 15 * time.Second
	DefaultPollTimeout  = 30 * time.Minute

	// FallbackMaxFee is used when /v1/estimate is unavailable (2.01 USDC).
	FallbackMaxFee = 2_010_000
)

// FallbackMaxBlockHeight is type(uint256).max, used when /v1/estimate is unavailable.
var FallbackMaxBlockHeight = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

const gatewayMinterABI = `[{"type":"function","name":"gatewayMint","inputs":[{"name":"attestationPayload","type":"bytes"},{"name":"signature","type":"bytes"}],"outputs":[],"stateMutability":"nonpayable"}]`

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

type Config struct {
	BaseURL    string             // Defaults to TestnetBaseURL.
	PrivateKey *ecdsa.PrivateKey
	HTTPClient *http.Client       // Defaults to a 30 s timeout client.
}

type Client struct {
	baseURL    string
	key        *ecdsa.PrivateKey
	signer     common.Address
	httpClient *http.Client
	minterABI  abi.ABI
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = TestnetBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	parsed, err := abi.JSON(strings.NewReader(gatewayMinterABI))
	if err != nil {
		return nil, fmt.Errorf("parse minter ABI: %w", err)
	}

	return &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		key:        cfg.PrivateKey,
		signer:     crypto.PubkeyToAddress(cfg.PrivateKey.PublicKey),
		httpClient: cfg.HTTPClient,
		minterABI:  parsed,
	}, nil
}

// SignerAddress returns the address derived from the client's private key.
func (c *Client) SignerAddress() common.Address { return c.signer }

// ---------------------------------------------------------------------------
// Gateway API methods
// ---------------------------------------------------------------------------

func (c *Client) GetInfo(ctx context.Context) (*InfoResponse, error) {
	var resp InfoResponse
	if err := c.doGet(ctx, "/v1/info", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetBalances(ctx context.Context, req *BalancesRequest) (*BalancesResponse, error) {
	var resp BalancesResponse
	if err := c.doPost(ctx, "/v1/balances", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetDeposits(ctx context.Context, req *DepositsRequest) (*DepositsResponse, error) {
	var resp DepositsResponse
	if err := c.doPost(ctx, "/v1/deposits", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Estimate calls POST /v1/estimate. Falls back to hardcoded values on error.
func (c *Client) Estimate(ctx context.Context, intents []BurnIntent, opts *EstimateOptions) (*EstimateResponse, error) {
	path := "/v1/estimate" + buildQueryString(opts.queryParams())
	var resp EstimateResponse
	if err := c.doPost(ctx, path, intents, &resp); err != nil {
		return c.estimateFallback(intents), nil
	}
	return &resp, nil
}

func (c *Client) estimateFallback(intents []BurnIntent) *EstimateResponse {
	resp := &EstimateResponse{}
	for _, intent := range intents {
		intent.MaxBlockHeight = NewBigIntFromBig(FallbackMaxBlockHeight)
		intent.MaxFee = NewBigInt(FallbackMaxFee)
		resp.Body = append(resp.Body, EstimateBody{BurnIntent: intent})
	}
	return resp
}

// Transfer calls POST /v1/transfer with signed burn intents.
func (c *Client) Transfer(ctx context.Context, signed []SignedBurnIntentRequest, opts *TransferOptions) (*TransferResponse, error) {
	path := "/v1/transfer" + buildQueryString(opts.queryParams())
	var resp TransferResponse
	if err := c.doPost(ctx, path, signed, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetTransferStatus(ctx context.Context, transferID string) (*TransferStatusResponse, error) {
	var resp TransferStatusResponse
	if err := c.doGet(ctx, "/v1/transfer/"+transferID, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PollTransfer polls GET /v1/transfer/{id} until a terminal status or timeout.
func (c *Client) PollTransfer(ctx context.Context, transferID string, interval, timeout time.Duration) (*TransferStatusResponse, error) {
	if interval == 0 {
		interval = DefaultPollInterval
	}
	if timeout == 0 {
		timeout = DefaultPollTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		status, err := c.GetTransferStatus(ctx, transferID)
		if err != nil {
			return nil, err
		}
		switch status.Status {
		case "confirmed", "finalized", "failed", "expired":
			return status, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("polling timed out after %v (last status: %s)", timeout, status.Status)
		case <-ticker.C:
		}
	}
}

// ---------------------------------------------------------------------------
// Transfer building & signing helpers
// ---------------------------------------------------------------------------

// BuildTransferSpec creates a TransferSpec from user-friendly parameters.
// Salt is generated randomly. SourceSigner is set to the client's address.
func (c *Client) BuildTransferSpec(params TransferSpecParams) (*TransferSpec, error) {
	var salt common.Hash
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	hookData := params.HookData
	if hookData == nil {
		hookData = []byte{}
	}

	return &TransferSpec{
		Version:              1,
		SourceDomain:         params.SourceDomain,
		DestinationDomain:    params.DestinationDomain,
		SourceContract:       AddressToBytes32(params.SourceWallet),
		DestinationContract:  AddressToBytes32(params.DestinationMinter),
		SourceToken:          AddressToBytes32(params.SourceToken),
		DestinationToken:     AddressToBytes32(params.DestinationToken),
		SourceDepositor:      AddressToBytes32(params.Depositor),
		DestinationRecipient: AddressToBytes32(params.Recipient),
		SourceSigner:         AddressToBytes32(c.signer),
		DestinationCaller:    AddressToBytes32(params.DestinationCaller),
		Value:                NewBigIntFromBig(params.Value),
		Salt:                 salt,
		HookData:             hookData,
	}, nil
}

// SignBurnIntent signs a BurnIntent with the client's private key.
func (c *Client) SignBurnIntent(intent *BurnIntent) ([]byte, error) {
	return SignBurnIntent(intent, c.key)
}

// SignBurnIntentSet signs a BurnIntentSet with the client's private key.
func (c *Client) SignBurnIntentSet(set *BurnIntentSet) ([]byte, error) {
	return SignBurnIntentSet(set, c.key)
}

// ---------------------------------------------------------------------------
// On-chain interaction
// ---------------------------------------------------------------------------

// SubmitMint sends a gatewayMint transaction to the GatewayMinter contract.
func (c *Client) SubmitMint(ctx context.Context, rpcURL string, minterAddr common.Address, attestation, sig []byte) (common.Hash, error) {
	ethClient, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return common.Hash{}, fmt.Errorf("dial RPC: %w", err)
	}
	defer ethClient.Close()

	chainID, err := ethClient.ChainID(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("get chain ID: %w", err)
	}

	auth, err := bind.NewKeyedTransactorWithChainID(c.key, chainID)
	if err != nil {
		return common.Hash{}, fmt.Errorf("create transactor: %w", err)
	}
	auth.Context = ctx

	contract := bind.NewBoundContract(minterAddr, c.minterABI, ethClient, ethClient, ethClient)
	tx, err := contract.Transact(auth, "gatewayMint", attestation, sig)
	if err != nil {
		return common.Hash{}, fmt.Errorf("submit gatewayMint: %w", err)
	}
	return tx.Hash(), nil
}

// ---------------------------------------------------------------------------
// HTTP plumbing
// ---------------------------------------------------------------------------

// APIError is returned for non-2xx responses from the Gateway API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gateway API %d: %s", e.StatusCode, e.Body)
}

func (c *Client) doGet(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, result)
}

func (c *Client) doPost(ctx context.Context, path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, result)
}

func (c *Client) do(req *http.Request, result any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Query-string helpers
// ---------------------------------------------------------------------------

type queryParam struct{ key, value string }

func (o *EstimateOptions) queryParams() []queryParam {
	if o == nil {
		return nil
	}
	var p []queryParam
	if o.EnableForwarder {
		p = append(p, queryParam{"enableForwarder", "true"})
	}
	if o.MaxAttestationSize > 0 {
		p = append(p, queryParam{"maxAttestationSize", fmt.Sprint(o.MaxAttestationSize)})
	}
	return p
}

func (o *TransferOptions) queryParams() []queryParam {
	if o == nil {
		return nil
	}
	var p []queryParam
	if o.EnableForwarder {
		p = append(p, queryParam{"enableForwarder", "true"})
	}
	if o.MaxAttestationSize > 0 {
		p = append(p, queryParam{"maxAttestationSize", fmt.Sprint(o.MaxAttestationSize)})
	}
	return p
}

func buildQueryString(params []queryParam) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = p.key + "=" + p.value
	}
	return "?" + strings.Join(parts, "&")
}

