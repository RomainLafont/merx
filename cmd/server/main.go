// Local API server exposing CCTP + Uniswap + Invoice operations for the frontend.
//
// Usage:
//
//	PRIVATE_KEY=0x... go run cmd/server/main.go
//	PRIVATE_KEY=0x... go run cmd/server/main.go --port 3001
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	merx "github.com/RomainLafont/merx"
	"gopkg.in/yaml.v3"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/google/uuid"
)

const payWithPermitABIJSON = `[{"type":"function","name":"payWithPermit","inputs":[{"name":"owner","type":"address"},{"name":"amount","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"},{"name":"maxFee","type":"uint256"}],"outputs":[]}]`

// uniswapConfig mirrors the YAML structure in uniswap-api/config.yaml.
type uniswapConfig struct {
	APIKey         string `yaml:"uniswap_api_key"`
	SwapperAddress string `yaml:"swapper_address"`
	BaseURL        string `yaml:"base_url"`
}

func loadUniswapConfig(path string) (*uniswapConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg uniswapConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("uniswap_api_key is required in %s", path)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://trade-api.gateway.uniswap.org/v1"
	}
	return &cfg, nil
}


const depositForBurnABIJSON = `[{"type":"function","name":"depositForBurn","inputs":[{"name":"amount","type":"uint256"},{"name":"destinationDomain","type":"uint32"},{"name":"mintRecipient","type":"bytes32"},{"name":"burnToken","type":"address"},{"name":"destinationCaller","type":"bytes32"},{"name":"maxFee","type":"uint256"},{"name":"minFinalityThreshold","type":"uint32"}],"outputs":[]}]`

const receiveMessageABIJSON = `[{"type":"function","name":"receiveMessage","inputs":[{"name":"message","type":"bytes"},{"name":"attestation","type":"bytes"}],"outputs":[{"name":"success","type":"bool"}]}]`

const relayAndSupplyABIJSON = `[{"type":"function","name":"relayAndSupply","inputs":[{"name":"message","type":"bytes"},{"name":"attestation","type":"bytes"},{"name":"beneficiary","type":"address"}],"outputs":[]}]`

// ---------------------------------------------------------------------------
// Token registry (loaded from registry.yaml)
// ---------------------------------------------------------------------------

type tokenEntry struct {
	Symbol   string `yaml:"symbol" json:"symbol"`
	Decimals int    `yaml:"decimals" json:"decimals"`
	Address  string `yaml:"address" json:"address"`
}

type registryChain struct {
	Name             string       `yaml:"name" json:"name"`
	ChainID          int          `yaml:"chainId" json:"chainId"`
	CCTPDomain       int          `yaml:"cctpDomain" json:"cctpDomain"`
	RPC              string       `yaml:"rpc" json:"-"`
	Explorer         string       `yaml:"explorer" json:"explorer"`
	UniswapSupported bool         `yaml:"uniswapSupported" json:"uniswapSupported"`
	Tokens           []tokenEntry `yaml:"tokens" json:"tokens"`
}

type registry struct {
	Chains []registryChain `yaml:"chains"`
}

var loadedRegistry *registry

func loadRegistry(path string) (*registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var reg registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func usdcAddressForChain(chainID int) string {
	if loadedRegistry == nil {
		return ""
	}
	for _, c := range loadedRegistry.Chains {
		if c.ChainID == chainID {
			for _, t := range c.Tokens {
				if t.Symbol == "USDC" {
					return t.Address
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Invoice store
// ---------------------------------------------------------------------------

type Invoice struct {
	ID              string     `json:"id"`
	MerchantAddress string     `json:"merchantAddress"`
	Amount          string     `json:"amount"`      // base units (6 decimals)
	AmountHuman     string     `json:"amountHuman"` // e.g. "100.00"
	ChainID         int        `json:"chainId"`
	Description     string     `json:"description"`
	ProductID       string     `json:"productId,omitempty"`
	PayerAddress    string     `json:"payerAddress,omitempty"`
	Status          string     `json:"status"` // paid → bridging → attesting → settled
	TxHash          string     `json:"txHash,omitempty"`
	ArcTxHash       string     `json:"arcTxHash,omitempty"`
	RefundArcTxHash string     `json:"refundArcTxHash,omitempty"`
	RefundTxHash    string     `json:"refundTxHash,omitempty"`
	RefundChainID   int        `json:"refundChainId,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	PaidAt          *time.Time `json:"paidAt,omitempty"`
	SettledAt       *time.Time `json:"settledAt,omitempty"`
}

type invoiceStore struct {
	mu       sync.RWMutex
	invoices map[string]*Invoice
	path     string // JSON file for persistence
}

func newInvoiceStore(path string) *invoiceStore {
	s := &invoiceStore{invoices: make(map[string]*Invoice), path: path}
	s.load()
	return s
}

func (s *invoiceStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var invoices map[string]*Invoice
	if err := json.Unmarshal(data, &invoices); err != nil {
		log.Printf("[invoices] failed to load %s: %v", s.path, err)
		return
	}
	s.invoices = invoices
	log.Printf("[invoices] loaded %d invoices from %s", len(invoices), s.path)
}

// save writes the current invoices map to disk. Must be called with mu held.
func (s *invoiceStore) save() {
	data, err := json.MarshalIndent(s.invoices, "", "  ")
	if err != nil {
		log.Printf("[invoices] marshal error: %v", err)
		return
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		log.Printf("[invoices] write error: %v", err)
	}
}

func (s *invoiceStore) create(inv *Invoice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invoices[inv.ID] = inv
	s.save()
}

func (s *invoiceStore) get(id string) *Invoice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.invoices[id]
}

func (s *invoiceStore) updateStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.invoices[id]; ok {
		inv.Status = status
		s.save()
	}
}

func (s *invoiceStore) setArcTx(id, arcTxHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.invoices[id]; ok {
		inv.ArcTxHash = arcTxHash
		inv.Status = "settled"
		now := time.Now()
		inv.SettledAt = &now
		s.save()
	}
}

func (s *invoiceStore) setRefundArcTx(id, txHash string, chainID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.invoices[id]; ok {
		inv.RefundArcTxHash = txHash
		inv.RefundChainID = chainID
		s.save()
	}
}

func (s *invoiceStore) setRefundTx(id, txHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.invoices[id]; ok {
		inv.RefundTxHash = txHash
		s.save()
	}
}

func (s *invoiceStore) list(merchant string) []*Invoice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Invoice
	for _, inv := range s.invoices {
		if merchant == "" || strings.EqualFold(inv.MerchantAddress, merchant) {
			result = append(result, inv)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Uniswap proxy client
// ---------------------------------------------------------------------------

type uniswapProxy struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newUniswapProxy(apiKey string) *uniswapProxy {
	return &uniswapProxy{
		baseURL:    "https://trade-api.gateway.uniswap.org/v1",
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// forward sends a request to Uniswap API and writes the response directly.
func (u *uniswapProxy) forward(w http.ResponseWriter, path string, body []byte) {
	req, err := http.NewRequest(http.MethodPost, u.baseURL+path, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", u.apiKey)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "uniswap request: %v", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

type server struct {
	key               *ecdsa.PrivateKey
	signer            common.Address
	arcOperatorKey    *ecdsa.PrivateKey // operator on ArcReceiver (0x2A94...)
	paymasterABI      abi.ABI
	depositForBurnABI abi.ABI
	receiveMessageABI abi.ABI
	relayAndSupplyABI abi.ABI
	invoices          *invoiceStore
	uniswap           *uniswapProxy
}

func main() {
	port := flag.Int("port", 8080, "HTTP port")
	privKeyHex := flag.String("private-key", "", "hex private key (or PRIVATE_KEY env)")
	uniswapCfgPath := flag.String("uniswap-config", "uniswap-api/config.yaml", "path to uniswap config.yaml")
	registryPath := flag.String("registry", "registry.yaml", "path to token registry YAML")
	flag.Parse()

	// Load token registry.
	reg, err := loadRegistry(*registryPath)
	if err != nil {
		log.Fatalf("load registry: %v", err)
	}
	loadedRegistry = reg
	log.Printf("loaded %d chains from registry", len(reg.Chains))

	keyHex := *privKeyHex
	if keyHex == "" {
		keyHex = os.Getenv("PRIVATE_KEY")
	}
	if keyHex == "" {
		keyHex = merx.DefaultPrivateKey
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}

	signer := crypto.PubkeyToAddress(key.PublicKey)

	paymasterABI, err := abi.JSON(strings.NewReader(payWithPermitABIJSON))
	if err != nil {
		log.Fatalf("parse paymaster ABI: %v", err)
	}
	depositForBurnABI, err := abi.JSON(strings.NewReader(depositForBurnABIJSON))
	if err != nil {
		log.Fatalf("parse depositForBurn ABI: %v", err)
	}
	receiveMessageABI, err := abi.JSON(strings.NewReader(receiveMessageABIJSON))
	if err != nil {
		log.Fatalf("parse receiveMessage ABI: %v", err)
	}
	relayAndSupplyABI, err := abi.JSON(strings.NewReader(relayAndSupplyABIJSON))
	if err != nil {
		log.Fatalf("parse relayAndSupply ABI: %v", err)
	}

	// Uniswap proxy.
	var uniProxy *uniswapProxy
	uniCfg, err := loadUniswapConfig(*uniswapCfgPath)
	if err != nil {
		log.Printf("warning: uniswap config not loaded (%v) — uniswap endpoints disabled", err)
	} else {
		uniProxy = newUniswapProxy(uniCfg.APIKey)
		uniProxy.baseURL = uniCfg.BaseURL
		log.Println("uniswap proxy enabled")
	}

	// The ArcReceiver contract has operator = 0x2A94... (merx.DefaultPrivateKey).
	// We need this key to call relayAndDeposit on Arc.
	arcOpKey, err := crypto.HexToECDSA(strings.TrimPrefix(merx.DefaultPrivateKey, "0x"))
	if err != nil {
		log.Fatalf("invalid arc operator key: %v", err)
	}

	s := &server{
		key:               key,
		signer:            signer,
		arcOperatorKey:    arcOpKey,
		paymasterABI:      paymasterABI,
		depositForBurnABI: depositForBurnABI,
		receiveMessageABI: receiveMessageABI,
		relayAndSupplyABI: relayAndSupplyABI,
		invoices:          newInvoiceStore("invoices.json"),
		uniswap:           uniProxy,
	}

	mux := http.NewServeMux()

	// CCTP endpoints.
	mux.HandleFunc("GET /api/balances", s.handleBalances)
	mux.HandleFunc("GET /api/pay-tx", s.handlePayTx)
	mux.HandleFunc("POST /api/pay", s.handlePay)
	mux.HandleFunc("POST /api/refund", s.handleRefund)
	mux.HandleFunc("POST /api/supply", s.handleSweep)
	mux.HandleFunc("POST /api/withdraw", s.handleWithdraw)

	// Chain info.
	mux.HandleFunc("GET /api/chains", s.handleChains)

	// Invoice endpoints.
	mux.HandleFunc("POST /api/invoices", s.handleCreateInvoice)
	mux.HandleFunc("GET /api/invoices", s.handleListInvoices)
	mux.HandleFunc("GET /api/invoices/{id}", s.handleGetInvoice)

	// Ebook download.
	mux.HandleFunc("GET /api/ebooks/{invoiceId}", s.handleEbookDownload)

	// Dashboard.
	mux.HandleFunc("GET /api/merchant/balances", s.handleMerchantBalances)

	// Uniswap proxy endpoints.
	mux.HandleFunc("POST /api/uniswap/quote", s.handleUniswapQuote)
	mux.HandleFunc("POST /api/uniswap/approval", s.handleUniswapApproval)
	mux.HandleFunc("POST /api/uniswap/swap", s.handleUniswapSwap)

	log.Printf("signer: %s", signer.Hex())
	log.Printf("listening on :%d", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), logRequests(cors(mux))))
}

// ---------------------------------------------------------------------------
// Chain handler
// ---------------------------------------------------------------------------

func (s *server) handleChains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, loadedRegistry.Chains)
}

// ---------------------------------------------------------------------------
// Dashboard: merchant balances across all chains
// ---------------------------------------------------------------------------

type chainBalance struct {
	Chain         string `json:"chain"`
	ChainID       int    `json:"chainId"`
	Balance       string `json:"balance"`       // USDC base units (6 decimals)
	NativeBalance string `json:"nativeBalance"` // native token in wei
}

// GET /api/merchant/balances — USDC balances on all chains + Arc.
func (s *server) handleMerchantBalances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	balABI, _ := abi.JSON(strings.NewReader(balanceOfABIJSON))

	type rpcQuery struct {
		name    string
		chainID int
		rpcURL  string
		usdc    common.Address
	}

	// Build query list from registry + Arc.
	var queries []rpcQuery
	if loadedRegistry != nil {
		for _, rc := range loadedRegistry.Chains {
			if rc.RPC == "" {
				continue
			}
			for _, t := range rc.Tokens {
				if t.Symbol == "USDC" {
					queries = append(queries, rpcQuery{
						name:    rc.Name,
						chainID: rc.ChainID,
						rpcURL:  rc.RPC,
						usdc:    common.HexToAddress(t.Address),
					})
					break
				}
			}
		}
	}
	queries = append(queries, rpcQuery{
		name:    "Arc Testnet",
		chainID: int(merx.ArcChainID),
		rpcURL:  merx.ArcRPCURL,
		usdc:    merx.TestnetUSDC[merx.ArcDomain],
	})

	// Query all chains in parallel.
	type result struct {
		cb  chainBalance
		err error
	}
	ch := make(chan result, len(queries))

	for _, q := range queries {
		go func(q rpcQuery) {
			ethClient, err := ethclient.DialContext(ctx, q.rpcURL)
			if err != nil {
				ch <- result{err: err}
				return
			}
			defer ethClient.Close()

			// USDC balance.
			contract := bind.NewBoundContract(q.usdc, balABI, ethClient, ethClient, ethClient)
			var out []interface{}
			err = contract.Call(&bind.CallOpts{Context: ctx}, &out, "balanceOf", s.signer)
			if err != nil {
				ch <- result{err: err}
				return
			}
			usdcBal := out[0].(*big.Int)

			// Native token balance.
			nativeBal, err := ethClient.BalanceAt(ctx, s.signer, nil)
			if err != nil {
				nativeBal = big.NewInt(0)
			}

			ch <- result{cb: chainBalance{
				Chain:         q.name,
				ChainID:       q.chainID,
				Balance:       usdcBal.String(),
				NativeBalance: nativeBal.String(),
			}}
		}(q)
	}

	var balances []chainBalance
	var total big.Int
	for range queries {
		res := <-ch
		if res.err != nil {
			log.Printf("[merchant-balances] query error: %v", res.err)
			continue
		}
		balances = append(balances, res.cb)
		bal, _ := new(big.Int).SetString(res.cb.Balance, 10)
		if bal != nil {
			total.Add(&total, bal)
		}
	}

	// Query Compound V3 balance + APY on Ethereum Sepolia.
	compoundBalance := "0"
	compoundAPY := 0.0
	if merx.CompoundComet != (common.Address{}) {
		sepoliaRPC := merx.RPCURLs[11155111]
		if sepoliaClient, err := ethclient.DialContext(ctx, sepoliaRPC); err == nil {
			defer sepoliaClient.Close()

			cometABI, _ := abi.JSON(strings.NewReader(`[
				{"type":"function","name":"balanceOf","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
				{"type":"function","name":"getUtilization","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
				{"type":"function","name":"getSupplyRate","inputs":[{"name":"utilization","type":"uint256"}],"outputs":[{"name":"","type":"uint64"}]}
			]`))
			cometContract := bind.NewBoundContract(merx.CompoundComet, cometABI, sepoliaClient, sepoliaClient, sepoliaClient)

			// Balance.
			var balOut []interface{}
			if err := cometContract.Call(&bind.CallOpts{Context: ctx}, &balOut, "balanceOf", s.signer); err == nil {
				compoundBalance = balOut[0].(*big.Int).String()
			}

			// APY: getUtilization → getSupplyRate → annualize.
			var utilOut []interface{}
			if err := cometContract.Call(&bind.CallOpts{Context: ctx}, &utilOut, "getUtilization"); err == nil {
				util := utilOut[0].(*big.Int)
				var rateOut []interface{}
				if err := cometContract.Call(&bind.CallOpts{Context: ctx}, &rateOut, "getSupplyRate", util); err == nil {
					ratePerSec := rateOut[0].(uint64)
					secondsPerYear := 365.25 * 24 * 3600
					compoundAPY = (math.Pow(1+float64(ratePerSec)/1e18, secondsPerYear) - 1) * 100
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"merchant":    s.signer.Hex(),
		"total":       total.String(),
		"balances":    balances,
		"compound":    compoundBalance,
		"compoundAPY": fmt.Sprintf("%.4f", compoundAPY),
	})
}

// ---------------------------------------------------------------------------
// Invoice handlers
// ---------------------------------------------------------------------------

type createInvoiceRequest struct {
	MerchantAddress string `json:"merchantAddress"`
	Amount          string `json:"amount"` // human-readable e.g. "100.50"
	ChainID         int    `json:"chainId"`
	Description     string `json:"description"`
}

func (s *server) handleCreateInvoice(w http.ResponseWriter, r *http.Request) {
	var req createInvoiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.MerchantAddress == "" || req.Amount == "" || req.ChainID == 0 {
		writeError(w, http.StatusBadRequest, "merchantAddress, amount, and chainId are required")
		return
	}

	// Parse human-readable amount and convert to base units (6 decimals).
	amountFloat, ok := new(big.Float).SetString(req.Amount)
	if !ok || amountFloat.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}
	multiplier := new(big.Float).SetInt64(1_000_000)
	baseUnits, _ := new(big.Float).Mul(amountFloat, multiplier).Int(nil)

	inv := &Invoice{
		ID:              uuid.New().String(),
		MerchantAddress: req.MerchantAddress,
		Amount:          baseUnits.String(),
		AmountHuman:     req.Amount,
		ChainID:         req.ChainID,
		Description:     req.Description,
		Status:          "pending",
		CreatedAt:       time.Now(),
	}
	s.invoices.create(inv)

	log.Printf("invoice created: id=%s merchant=%s amount=%s USDC chain=%d", inv.ID, inv.MerchantAddress, inv.AmountHuman, inv.ChainID)
	writeJSON(w, http.StatusCreated, inv)
}

func (s *server) handleListInvoices(w http.ResponseWriter, r *http.Request) {
	merchant := r.URL.Query().Get("merchant")
	invoices := s.invoices.list(merchant)
	if invoices == nil {
		invoices = []*Invoice{}
	}
	writeJSON(w, http.StatusOK, invoices)
}

func (s *server) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inv := s.invoices.get(id)
	if inv == nil {
		writeError(w, http.StatusNotFound, "invoice not found: %s", id)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

// Product ID → ebook filename mapping.
var ebookFiles = map[string]string{
	"mastering-solidity": "mastering_solidity.pdf",
	"defi-handbook":      "defi_handbook.pdf",
	"zero-knowledge":     "zero_knowledge_proofs.pdf",
	"crypto-economics":   "cryptoeconomics_101.pdf",
	"nft-art":            "nfts_digital_art.pdf",
	"web3-security":      "web3_security_auditing.pdf",
}

func (s *server) handleEbookDownload(w http.ResponseWriter, r *http.Request) {
	invoiceID := r.PathValue("invoiceId")
	inv := s.invoices.get(invoiceID)
	if inv == nil {
		writeError(w, http.StatusNotFound, "invoice not found")
		return
	}

	// Only allow download for paid (non-refunded) invoices.
	if inv.Status == "pending" {
		writeError(w, http.StatusForbidden, "invoice not paid")
		return
	}
	if inv.RefundTxHash != "" || inv.RefundArcTxHash != "" {
		writeError(w, http.StatusForbidden, "invoice has been refunded")
		return
	}

	if inv.ProductID == "" {
		writeError(w, http.StatusNotFound, "no product associated")
		return
	}

	filename, ok := ebookFiles[inv.ProductID]
	if !ok {
		writeError(w, http.StatusNotFound, "ebook not found for product: %s", inv.ProductID)
		return
	}

	filepath := "ebooks/" + filename
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/pdf")
	http.ServeFile(w, r, filepath)
}

// ---------------------------------------------------------------------------
// Uniswap proxy handlers
// ---------------------------------------------------------------------------

type uniswapQuoteRequest struct {
	TokenIn        string `json:"tokenIn"`
	TokenInChainId int    `json:"tokenInChainId"`
	Amount         string `json:"amount"` // USDC amount in base units (EXACT_OUTPUT)
	Swapper        string `json:"swapper"`
}

func (s *server) handleUniswapQuote(w http.ResponseWriter, r *http.Request) {
	if s.uniswap == nil {
		writeError(w, http.StatusServiceUnavailable, "uniswap not configured")
		return
	}

	var req uniswapQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if req.TokenIn == "" || req.TokenInChainId == 0 || req.Amount == "" || req.Swapper == "" {
		writeError(w, http.StatusBadRequest, "tokenIn, tokenInChainId, amount, and swapper are required")
		return
	}

	usdcAddr := usdcAddressForChain(req.TokenInChainId)
	if usdcAddr == "" {
		writeError(w, http.StatusBadRequest, "unsupported chain: %d", req.TokenInChainId)
		return
	}

	// Build full Uniswap quote request.
	// Force CLASSIC routing (V2/V3/V4) so the quote works with /swap.
	// DutchQuotes (UniswapX) require /order instead and aren't supported here.
	fullReq := map[string]any{
		"type":              "EXACT_OUTPUT",
		"amount":            req.Amount,
		"tokenIn":           req.TokenIn,
		"tokenOut":          usdcAddr,
		"tokenInChainId":    req.TokenInChainId,
		"tokenOutChainId":   req.TokenInChainId,
		"swapper":           req.Swapper,
		"slippageTolerance": 0.5,
		"protocols":         []string{"V2", "V3", "V4"},
	}
	body, _ := json.Marshal(fullReq)
	s.uniswap.forward(w, "/quote", body)
}

func (s *server) handleUniswapApproval(w http.ResponseWriter, r *http.Request) {
	if s.uniswap == nil {
		writeError(w, http.StatusServiceUnavailable, "uniswap not configured")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: %v", err)
		return
	}
	s.uniswap.forward(w, "/check_approval", body)
}

func (s *server) handleUniswapSwap(w http.ResponseWriter, r *http.Request) {
	if s.uniswap == nil {
		writeError(w, http.StatusServiceUnavailable, "uniswap not configured")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: %v", err)
		return
	}
	s.uniswap.forward(w, "/swap", body)
}

// ---------------------------------------------------------------------------
// CCTP V2 helpers
// ---------------------------------------------------------------------------

// addressToBytes32 left-pads an address to 32 bytes for CCTP mintRecipient.
func addressToBytes32(addr common.Address) [32]byte {
	var b [32]byte
	copy(b[12:], addr.Bytes())
	return b
}

// balanceOfABIJSON is the ERC-20 balanceOf selector.
const balanceOfABIJSON = `[{"type":"function","name":"balanceOf","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]}]`

// GET /api/balances — query USDC.balanceOf(shopWallet) on Arc.
func (s *server) handleBalances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ethClient, err := ethclient.DialContext(ctx, merx.ArcRPCURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dial Arc RPC: %v", err)
		return
	}
	defer ethClient.Close()

	balABI, err := abi.JSON(strings.NewReader(balanceOfABIJSON))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "parse balanceOf ABI: %v", err)
		return
	}

	usdc := merx.TestnetUSDC[merx.ArcDomain]
	contract := bind.NewBoundContract(usdc, balABI, ethClient, ethClient, ethClient)

	var out []interface{}
	err = contract.Call(&bind.CallOpts{Context: ctx}, &out, "balanceOf", s.signer)
	if err != nil {
		writeError(w, http.StatusBadGateway, "balanceOf: %v", err)
		return
	}

	balance := out[0].(*big.Int)
	writeJSON(w, http.StatusOK, map[string]string{
		"wallet":  s.signer.Hex(),
		"token":   usdc.Hex(),
		"chain":   "arc",
		"balance": balance.String(),
	})
}

// GET /api/pay-tx?chain_id=1301&amount=1000000 — returns the permit data
// for the customer to sign off-chain, plus metadata.
//
// Flow:
//  1. Frontend calls GET /api/pay-tx
//  2. Customer signs the EIP-2612 permit (off-chain, gasless)
//  3. Frontend sends POST /api/pay with the signature
//  4. Backend broadcasts payWithPermit tx and pays gas
func (s *server) handlePayTx(w http.ResponseWriter, r *http.Request) {
	chainIDStr := r.URL.Query().Get("chain_id")
	amountStr := r.URL.Query().Get("amount")

	if chainIDStr == "" || amountStr == "" {
		writeError(w, http.StatusBadRequest, "chain_id and amount are required")
		return
	}

	chainID, err := strconv.ParseUint(chainIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chain_id: %s", chainIDStr)
		return
	}

	if _, ok := merx.ChainIDToDomain[chainID]; !ok {
		writeError(w, http.StatusBadRequest, "unsupported chain_id: %d", chainID)
		return
	}

	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", amountStr)
		return
	}

	paymasterAddr, ok := merx.ShopPaymaster[chainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "no ShopPaymaster deployed for chain_id %d", chainID)
		return
	}

	domain := merx.ChainIDToDomain[chainID]
	usdc, ok := merx.TestnetUSDC[domain]
	if !ok {
		writeError(w, http.StatusBadRequest, "no USDC address for domain %d", domain)
		return
	}

	// Deadline: 1 hour from now.
	deadline := big.NewInt(time.Now().Add(1 * time.Hour).Unix())

	writeJSON(w, http.StatusOK, payTxResponse{
		ChainID:  chainID,
		Amount:   amount.String(),
		Deadline: deadline.String(),
		Permit: permitData{
			Token:   usdc.Hex(),
			Spender: paymasterAddr.Hex(),
			Domain: permitDomain{
				Name:              "USDC",
				Version:           "2",
				ChainID:           chainID,
				VerifyingContract: usdc.Hex(),
			},
		},
	})
}

type payTxResponse struct {
	ChainID  uint64     `json:"chain_id"`
	Amount   string     `json:"amount"`
	Deadline string     `json:"deadline"`
	Permit   permitData `json:"permit"`
}

type permitData struct {
	Token   string       `json:"token"`
	Spender string       `json:"spender"`
	Domain  permitDomain `json:"domain"`
}

type permitDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chain_id"`
	VerifyingContract string `json:"verifying_contract"`
}

// POST /api/pay — receive the signed permit and broadcast payWithPermit.
func (s *server) handlePay(w http.ResponseWriter, r *http.Request) {
	var req payRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.Owner == "" || req.Amount == "" || req.Deadline == "" || req.Signature == "" {
		writeError(w, http.StatusBadRequest, "owner, amount, deadline, and signature are required")
		return
	}

	if req.ChainID == 0 {
		writeError(w, http.StatusBadRequest, "chain_id is required")
		return
	}

	paymasterAddr, ok := merx.ShopPaymaster[req.ChainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "no ShopPaymaster for chain_id %d", req.ChainID)
		return
	}

	rpcURL, ok := merx.RPCURLs[req.ChainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "no RPC URL for chain_id %d", req.ChainID)
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}

	deadline, ok := new(big.Int).SetString(req.Deadline, 10)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid deadline: %s", req.Deadline)
		return
	}

	// Decode signature (65 bytes: r[32] + s[32] + v[1]).
	sig, err := hexutil.Decode(req.Signature)
	if err != nil || len(sig) != 65 {
		writeError(w, http.StatusBadRequest, "invalid signature: expected 65 bytes hex")
		return
	}

	// Split into v, r, s. go-ethereum returns v as 0/1; EIP-2612 expects 27/28.
	v := sig[64]
	if v < 27 {
		v += 27
	}
	var rBytes, sBytes [32]byte
	copy(rBytes[:], sig[:32])
	copy(sBytes[:], sig[32:64])

	owner := common.HexToAddress(req.Owner)
	maxFee := merx.DefaultMaxFee

	// ABI-encode payWithPermit(owner, amount, deadline, v, r, s, maxFee).
	calldata, err := s.paymasterABI.Pack("payWithPermit",
		owner,
		amount,
		deadline,
		v,
		rBytes,
		sBytes,
		maxFee,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack calldata: %v", err)
		return
	}

	// Connect to RPC and broadcast.
	ethClient, err := ethclient.DialContext(r.Context(), rpcURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dial RPC: %v", err)
		return
	}
	defer ethClient.Close()

	chainIDBig, err := ethClient.ChainID(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "get chain ID: %v", err)
		return
	}

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, chainIDBig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create transactor: %v", err)
		return
	}
	auth.Context = r.Context()

	contract := bind.NewBoundContract(paymasterAddr, s.paymasterABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast tx: %v", err)
		return
	}

	txHash := tx.Hash().Hex()
	sourceDomain := merx.ChainIDToDomain[req.ChainID]

	// Create invoice now that the tx is broadcast.
	amountHuman := fmt.Sprintf("%.2f", float64(amount.Int64())/1_000_000)
	inv := &Invoice{
		ID:              uuid.New().String(),
		MerchantAddress: s.signer.Hex(),
		ProductID:       req.ProductID,
		PayerAddress:    req.Owner,
		Amount:          req.Amount,
		AmountHuman:     amountHuman,
		ChainID:         int(req.ChainID),
		Description:     req.Description,
		Status:          "paid",
		TxHash:          txHash,
		CreatedAt:       time.Now(),
	}
	now := time.Now()
	inv.PaidAt = &now
	s.invoices.create(inv)

	log.Printf("payment broadcast: tx=%s owner=%s chain=%d amount=%s invoice=%s", txHash, req.Owner, req.ChainID, req.Amount, inv.ID)

	// Background: poll CCTP attestation, then self-relay receiveMessage on Arc.
	go s.pollAndRelay(sourceDomain, txHash, inv.ID)

	writeJSON(w, http.StatusCreated, payResponse{
		TxHash:    txHash,
		ChainID:   req.ChainID,
		InvoiceID: inv.ID,
	})
}

// pollAndRelay waits for the CCTP attestation, then calls
// MessageTransmitter.receiveMessage on Arc to mint USDC to the shop wallet.
// Updates invoice status along the way.
func (s *server) pollAndRelay(sourceDomain uint32, txHash string, invoiceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.invoices.updateStatus(invoiceID, "bridging")
	log.Printf("[relay] waiting for CCTP attestation: domain=%d tx=%s invoice=%s", sourceDomain, txHash, invoiceID)

	s.invoices.updateStatus(invoiceID, "attesting")
	message, attestation, err := s.pollCCTPAttestation(ctx, sourceDomain, txHash)
	if err != nil {
		log.Printf("[relay] %v", err)
		return
	}

	// Self-relay on Arc: call MessageTransmitter.receiveMessage.
	relayTx, err := s.relayCCTP(ctx, merx.ArcRPCURL, big.NewInt(merx.ArcChainID), merx.MessageTransmitter, message, attestation)
	if err != nil {
		log.Printf("[relay] ERROR: %v", err)
		return
	}
	s.invoices.setArcTx(invoiceID, relayTx)
	log.Printf("[relay] settled on Arc: tx=%s (source tx=%s) invoice=%s", relayTx, txHash, invoiceID)
}

// pollCCTPAttestation polls the CCTP attestation API until status=complete,
// returning the raw message and attestation hex strings.
func (s *server) pollCCTPAttestation(ctx context.Context, sourceDomain uint32, txHash string) (string, string, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("timeout waiting for attestation: tx=%s", txHash)
		case <-ticker.C:
		}

		msg, att, status, err := s.fetchCCTPAttestation(ctx, sourceDomain, txHash)
		if err != nil {
			log.Printf("[cctp] poll error: %v", err)
			continue
		}
		if status == "" {
			continue // not found yet
		}
		log.Printf("[cctp] status=%s tx=%s", status, txHash)
		if status == "complete" {
			return msg, att, nil
		}
	}
}

// fetchCCTPAttestation makes a single request to the CCTP attestation API.
func (s *server) fetchCCTPAttestation(ctx context.Context, sourceDomain uint32, txHash string) (string, string, string, error) {
	url := fmt.Sprintf("%s/%d?transactionHash=%s", merx.CCTPAttestationURL, sourceDomain, txHash)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", nil // not found yet
	}

	var result struct {
		Messages []struct {
			Message     string `json:"message"`
			Attestation string `json:"attestation"`
			Status      string `json:"status"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", "", err
	}
	if len(result.Messages) == 0 {
		return "", "", "", nil
	}
	m := result.Messages[0]
	return m.Message, m.Attestation, m.Status, nil
}

// relayCCTP calls MessageTransmitter.receiveMessage on the given chain.
// Returns the relay tx hash.
func (s *server) relayCCTP(ctx context.Context, rpcURL string, chainID *big.Int, transmitter common.Address, message, attestation string) (string, error) {
	msgBytes, err := hexutil.Decode(message)
	if err != nil {
		return "", fmt.Errorf("decode message: %w", err)
	}
	attBytes, err := hexutil.Decode(attestation)
	if err != nil {
		return "", fmt.Errorf("decode attestation: %w", err)
	}

	calldata, err := s.receiveMessageABI.Pack("receiveMessage", msgBytes, attBytes)
	if err != nil {
		return "", fmt.Errorf("pack receiveMessage: %w", err)
	}

	ethClient, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return "", fmt.Errorf("dial RPC: %w", err)
	}
	defer ethClient.Close()

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, chainID)
	if err != nil {
		return "", fmt.Errorf("create transactor: %w", err)
	}
	auth.Context = ctx

	contract := bind.NewBoundContract(transmitter, s.receiveMessageABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		return "", fmt.Errorf("broadcast receiveMessage: %w", err)
	}

	return tx.Hash().Hex(), nil
}

type payRequest struct {
	Owner       string `json:"owner"`
	ChainID     uint64 `json:"chain_id"`
	Amount      string `json:"amount"`
	Deadline    string `json:"deadline"`
	Signature   string `json:"signature"`
	Description string `json:"description"`
	ProductID   string `json:"productId"`
}

type payResponse struct {
	TxHash    string `json:"tx_hash"`
	ChainID   uint64 `json:"chain_id"`
	InvoiceID string `json:"invoice_id"`
}

type refundRequest struct {
	InvoiceID string `json:"invoiceId"`
	To        string `json:"to"`
	ChainID   uint64 `json:"chainId"`
	Amount    string `json:"amount"`
}

type refundResponse struct {
	TxHash string `json:"txHash"`
}

func (s *server) handleRefund(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.To == "" || req.ChainID == 0 || req.Amount == "" {
		writeError(w, http.StatusBadRequest, "to, chainId, and amount are required")
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}

	dstDomain, ok := merx.ChainIDToDomain[req.ChainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported chainId: %d", req.ChainID)
		return
	}

	dstRPC, ok := merx.RPCURLs[req.ChainID]
	if !ok {
		writeError(w, http.StatusBadRequest, "no RPC URL for chainId %d", req.ChainID)
		return
	}

	recipient := common.HexToAddress(req.To)
	ctx := r.Context()

	// Check shop USDC balance on Arc.
	arcBalance, err := s.getArcUSDCBalance(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "check balance: %v", err)
		return
	}
	if arcBalance.Cmp(amount) < 0 {
		writeError(w, http.StatusConflict, "insufficient USDC on Arc: have %s, need %s", arcBalance, amount)
		return
	}

	// depositForBurn on Arc → customer's chain.
	mintRecipient := addressToBytes32(recipient)
	burnToken := merx.TestnetUSDC[merx.ArcDomain]
	var zeroCaller [32]byte

	calldata, err := s.depositForBurnABI.Pack("depositForBurn",
		amount,
		dstDomain,
		mintRecipient,
		burnToken,
		zeroCaller,
		merx.DefaultMaxFee,
		uint32(0), // minFinalityThreshold
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack depositForBurn: %v", err)
		return
	}

	ethClient, err := ethclient.DialContext(ctx, merx.ArcRPCURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dial Arc RPC: %v", err)
		return
	}
	defer ethClient.Close()

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, big.NewInt(merx.ArcChainID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create transactor: %v", err)
		return
	}
	auth.Context = ctx

	contract := bind.NewBoundContract(merx.TokenMessengerV2, s.depositForBurnABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast depositForBurn: %v", err)
		return
	}

	txHash := tx.Hash().Hex()
	log.Printf("refund started: tx=%s to=%s chain=%d amount=%s invoice=%s", txHash, req.To, req.ChainID, req.Amount, req.InvoiceID)

	// Mark invoice as refunded with the Arc burn tx.
	if req.InvoiceID != "" {
		s.invoices.setRefundArcTx(req.InvoiceID, txHash, int(req.ChainID))
	}

	// Background: poll CCTP attestation, then self-relay receiveMessage on destination.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		message, attestation, err := s.pollCCTPAttestation(bgCtx, merx.ArcDomain, txHash)
		if err != nil {
			log.Printf("[refund-relay] %v", err)
			return
		}

		relayTx, err := s.relayCCTP(bgCtx, dstRPC, new(big.Int).SetUint64(req.ChainID), merx.MessageTransmitter, message, attestation)
		if err != nil {
			log.Printf("[refund-relay] ERROR: %v", err)
			return
		}
		log.Printf("[refund-relay] receiveMessage: tx=%s (burn tx=%s)", relayTx, txHash)

		// Update invoice with the destination chain relay tx.
		if req.InvoiceID != "" {
			s.invoices.setRefundTx(req.InvoiceID, relayTx)
		}
	}()

	writeJSON(w, http.StatusCreated, refundResponse{TxHash: txHash})
}

// POST /api/sweep — sweep USDC from Arc into Compound V3 on Ethereum Sepolia.
func (s *server) handleSweep(w http.ResponseWriter, r *http.Request) {
	var req sweepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.Amount == "" {
		writeError(w, http.StatusBadRequest, "amount is required")
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}

	if merx.CompoundDepositor == (common.Address{}) {
		writeError(w, http.StatusBadRequest, "CompoundDepositor not deployed")
		return
	}

	ctx := r.Context()
	const dstDomainID uint32 = 0 // Ethereum Sepolia

	// Check shop USDC balance on Arc.
	arcBalance, err := s.getArcUSDCBalance(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "check balance: %v", err)
		return
	}
	if arcBalance.Cmp(amount) < 0 {
		writeError(w, http.StatusConflict, "insufficient USDC on Arc: have %s, need %s", arcBalance, amount)
		return
	}

	// depositForBurn on Arc → Ethereum Sepolia, mintRecipient = CompoundDepositor.
	mintRecipient := addressToBytes32(merx.CompoundDepositor)
	burnToken := merx.TestnetUSDC[merx.ArcDomain]
	var zeroCaller [32]byte

	calldata, err := s.depositForBurnABI.Pack("depositForBurn",
		amount,
		dstDomainID,
		mintRecipient,
		burnToken,
		zeroCaller,
		merx.DefaultMaxFee,
		uint32(0), // minFinalityThreshold
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack depositForBurn: %v", err)
		return
	}

	ethClient, err := ethclient.DialContext(ctx, merx.ArcRPCURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dial Arc RPC: %v", err)
		return
	}
	defer ethClient.Close()

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, big.NewInt(merx.ArcChainID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create transactor: %v", err)
		return
	}
	auth.Context = ctx

	contract := bind.NewBoundContract(merx.TokenMessengerV2, s.depositForBurnABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast depositForBurn: %v", err)
		return
	}

	txHash := tx.Hash().Hex()
	log.Printf("sweep started: tx=%s amount=%s → Compound V3", txHash, req.Amount)

	// Background: poll CCTP attestation, then call CompoundDepositor.relayAndSupply on Ethereum Sepolia.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		message, attestation, err := s.pollCCTPAttestation(bgCtx, merx.ArcDomain, txHash)
		if err != nil {
			log.Printf("[sweep] %v", err)
			return
		}

		msgBytes, err := hexutil.Decode(message)
		if err != nil {
			log.Printf("[sweep] decode message: %v", err)
			return
		}
		attBytes, err := hexutil.Decode(attestation)
		if err != nil {
			log.Printf("[sweep] decode attestation: %v", err)
			return
		}

		supplyCalldata, err := s.relayAndSupplyABI.Pack("relayAndSupply", msgBytes, attBytes, s.signer)
		if err != nil {
			log.Printf("[sweep] pack relayAndSupply: %v", err)
			return
		}

		rpcURL := merx.RPCURLs[11155111]
		sepoliaClient, err := ethclient.DialContext(bgCtx, rpcURL)
		if err != nil {
			log.Printf("[sweep] dial Sepolia RPC: %v", err)
			return
		}
		defer sepoliaClient.Close()

		supplyAuth, err := bind.NewKeyedTransactorWithChainID(s.key, big.NewInt(11155111))
		if err != nil {
			log.Printf("[sweep] create transactor: %v", err)
			return
		}
		supplyAuth.Context = bgCtx

		depositorContract := bind.NewBoundContract(merx.CompoundDepositor, s.relayAndSupplyABI, sepoliaClient, sepoliaClient, sepoliaClient)
		supplyTx, err := depositorContract.RawTransact(supplyAuth, supplyCalldata)
		if err != nil {
			log.Printf("[sweep] relayAndSupply failed: %v", err)
			return
		}

		log.Printf("[sweep] relayAndSupply broadcast: tx=%s (burn tx=%s)", supplyTx.Hash().Hex(), txHash)
	}()

	writeJSON(w, http.StatusCreated, sweepResponse{TxHash: txHash})
}

type sweepRequest struct {
	Amount string `json:"amount"`
}

type sweepResponse struct {
	TxHash string `json:"txHash"`
}

// POST /api/withdraw — withdraw USDC from Compound V3 on Ethereum Sepolia, bridge back to Arc.
func (s *server) handleWithdraw(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.Amount == "" {
		writeError(w, http.StatusBadRequest, "amount is required")
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}

	ctx := r.Context()
	sepoliaRPC := merx.RPCURLs[11155111]

	ethClient, err := ethclient.DialContext(ctx, sepoliaRPC)
	if err != nil {
		writeError(w, http.StatusBadGateway, "dial Sepolia RPC: %v", err)
		return
	}
	defer ethClient.Close()

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, big.NewInt(11155111))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create transactor: %v", err)
		return
	}
	auth.Context = ctx

	// Step 1: withdraw from Compound V3.
	withdrawABI, _ := abi.JSON(strings.NewReader(`[{"type":"function","name":"withdraw","inputs":[{"name":"asset","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[]}]`))
	sepoliaUSDC := merx.TestnetUSDC[0] // domain 0 = Ethereum Sepolia

	withdrawData, err := withdrawABI.Pack("withdraw", sepoliaUSDC, amount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack withdraw: %v", err)
		return
	}

	cometContract := bind.NewBoundContract(merx.CompoundComet, withdrawABI, ethClient, ethClient, ethClient)
	withdrawTx, err := cometContract.RawTransact(auth, withdrawData)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast withdraw: %v", err)
		return
	}

	log.Printf("[withdraw] Compound withdraw broadcast: %s amount=%s", withdrawTx.Hash().Hex(), req.Amount)

	// Wait for withdraw to be mined before bridging.
	receipt, err := bind.WaitMined(ctx, ethClient, withdrawTx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "withdraw wait: %v", err)
		return
	}
	if receipt.Status != 1 {
		writeError(w, http.StatusBadGateway, "withdraw reverted")
		return
	}

	// Step 2: approve TokenMessenger on Eth Sepolia.
	erc20ABI, _ := abi.JSON(strings.NewReader(`[{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}]`))
	approveData, err := erc20ABI.Pack("approve", merx.TokenMessengerV2, amount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack approve: %v", err)
		return
	}

	auth.Nonce = nil
	approveTx, err := bind.NewBoundContract(sepoliaUSDC, erc20ABI, ethClient, ethClient, ethClient).RawTransact(auth, approveData)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast approve: %v", err)
		return
	}

	approveReceipt, err := bind.WaitMined(ctx, ethClient, approveTx)
	if err != nil || approveReceipt.Status != 1 {
		writeError(w, http.StatusBadGateway, "approve failed")
		return
	}

	// Step 3: depositForBurn on Eth Sepolia → Arc.
	mintRecipient := addressToBytes32(s.signer)
	var zeroCaller [32]byte

	burnData, err := s.depositForBurnABI.Pack("depositForBurn",
		amount,
		merx.ArcDomain,
		mintRecipient,
		sepoliaUSDC,
		zeroCaller,
		merx.DefaultMaxFee,
		uint32(0),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack depositForBurn: %v", err)
		return
	}

	auth.Nonce = nil
	burnTx, err := bind.NewBoundContract(merx.TokenMessengerV2, s.depositForBurnABI, ethClient, ethClient, ethClient).RawTransact(auth, burnData)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast depositForBurn: %v", err)
		return
	}

	txHash := burnTx.Hash().Hex()
	log.Printf("[withdraw] depositForBurn broadcast: %s → Arc", txHash)

	// Background: poll CCTP attestation, then self-relay on Arc.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		message, attestation, err := s.pollCCTPAttestation(bgCtx, 0, txHash) // domain 0 = Eth Sepolia
		if err != nil {
			log.Printf("[withdraw] %v", err)
			return
		}

		relayTx, err := s.relayCCTP(bgCtx, merx.ArcRPCURL, big.NewInt(merx.ArcChainID), merx.MessageTransmitter, message, attestation)
		if err != nil {
			log.Printf("[withdraw] relay on Arc: %v", err)
			return
		}
		log.Printf("[withdraw] settled on Arc: %s", relayTx)
	}()

	writeJSON(w, http.StatusCreated, map[string]string{"txHash": txHash})
}

// getArcUSDCBalance returns the shop wallet's USDC balance on Arc.
func (s *server) getArcUSDCBalance(ctx context.Context) (*big.Int, error) {
	ethClient, err := ethclient.DialContext(ctx, merx.ArcRPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial Arc RPC: %w", err)
	}
	defer ethClient.Close()

	balABI, err := abi.JSON(strings.NewReader(balanceOfABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse balanceOf ABI: %w", err)
	}

	usdc := merx.TestnetUSDC[merx.ArcDomain]
	contract := bind.NewBoundContract(usdc, balABI, ethClient, ethClient, ethClient)

	var out []interface{}
	err = contract.Call(&bind.CallOpts{Context: ctx}, &out, "balanceOf", s.signer)
	if err != nil {
		return nil, fmt.Errorf("balanceOf: %w", err)
	}

	return out[0].(*big.Int), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s → %d (%s)", r.Method, r.URL.Path, rec.code, time.Since(start).Truncate(time.Millisecond))
	})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, errorResponse{Error: fmt.Sprintf(format, args...)})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
