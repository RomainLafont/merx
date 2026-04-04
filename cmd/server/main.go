// Local API server exposing CCTP + Uniswap + Invoice operations for the frontend.
//
// Usage:
//
//	go run cmd/server/main.go
//	PORT=3001 go run cmd/server/main.go
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
	"path"
	"path/filepath"
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

const depositForBurnWithHookABIJSON = `[{"type":"function","name":"depositForBurnWithHook","inputs":[{"name":"amount","type":"uint256"},{"name":"destinationDomain","type":"uint32"},{"name":"mintRecipient","type":"bytes32"},{"name":"burnToken","type":"address"},{"name":"destinationCaller","type":"bytes32"},{"name":"maxFee","type":"uint256"},{"name":"minFinalityThreshold","type":"uint32"},{"name":"hookData","type":"bytes"}],"outputs":[]}]`

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

func registryChainByID(chainID int) *registryChain {
	if loadedRegistry == nil {
		return nil
	}
	for i := range loadedRegistry.Chains {
		if loadedRegistry.Chains[i].ChainID == chainID {
			return &loadedRegistry.Chains[i]
		}
	}
	return nil
}

// cctpDomainForChain looks up the CCTP domain for a given chain ID from the
// loaded registry.
func cctpDomainForChain(chainID int) (uint32, bool) {
	if loadedRegistry == nil {
		return 0, false
	}
	for _, c := range loadedRegistry.Chains {
		if c.ChainID == chainID {
			return uint32(c.CCTPDomain), true
		}
	}
	return 0, false
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

func spaFileHandler(root string) http.Handler {
	fileServer := http.FileServer(http.Dir(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		cleanPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if cleanPath != "" {
			fullPath := filepath.Join(root, cleanPath)
			if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
			if filepath.Ext(cleanPath) != "" {
				http.NotFound(w, r)
				return
			}
		}

		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

type server struct {
	key                       *ecdsa.PrivateKey
	signer                    common.Address
	arcOperatorKey            *ecdsa.PrivateKey // operator on ArcReceiver (0x2A94...)
	depositForBurnABI         abi.ABI
	depositForBurnWithHookABI abi.ABI
	relayAndSupplyABI         abi.ABI
	invoices                  *invoiceStore
	uniswap                   *uniswapProxy
}

func main() {
	portDefault := 8080
	if portEnv := os.Getenv("PORT"); portEnv != "" {
		if parsedPort, err := strconv.Atoi(portEnv); err == nil && parsedPort > 0 {
			portDefault = parsedPort
		}
	}

	port := flag.Int("port", portDefault, "HTTP port")
	uniswapCfgPath := flag.String("uniswap-config", "uniswap-api/config.yaml", "path to uniswap config.yaml")
	registryPath := flag.String("registry", "registry.yaml", "path to token registry YAML")
	frontendDistPath := flag.String("frontend-dist", "frontend/dist", "path to built frontend assets")
	flag.Parse()

	// Load token registry.
	reg, err := loadRegistry(*registryPath)
	if err != nil {
		log.Fatalf("load registry: %v", err)
	}
	loadedRegistry = reg
	log.Printf("loaded %d chains from registry", len(reg.Chains))

	key, err := crypto.HexToECDSA(strings.TrimPrefix(merx.DefaultPrivateKey, "0x"))
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}

	signer := crypto.PubkeyToAddress(key.PublicKey)

	depositForBurnABI, err := abi.JSON(strings.NewReader(depositForBurnABIJSON))
	if err != nil {
		log.Fatalf("parse depositForBurn ABI: %v", err)
	}
	depositForBurnWithHookABI, err := abi.JSON(strings.NewReader(depositForBurnWithHookABIJSON))
	if err != nil {
		log.Fatalf("parse depositForBurnWithHook ABI: %v", err)
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
		key:                       key,
		signer:                    signer,
		arcOperatorKey:            arcOpKey,
		depositForBurnABI:         depositForBurnABI,
		depositForBurnWithHookABI: depositForBurnWithHookABI,
		relayAndSupplyABI:         relayAndSupplyABI,
		invoices:                  newInvoiceStore("invoices.json"),
		uniswap:                   uniProxy,
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
	mux.HandleFunc("GET /api/config", s.handleConfig)
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

	// Frontend build for the single-service POC deployment.
	if _, err := os.Stat(filepath.Join(*frontendDistPath, "index.html")); err == nil {
		mux.Handle("/", spaFileHandler(*frontendDistPath))
		log.Printf("frontend enabled from %s", *frontendDistPath)
	} else {
		log.Printf("warning: frontend dist not found at %s (%v)", *frontendDistPath, err)
	}

	log.Printf("signer: %s", signer.Hex())
	log.Printf("listening on :%d", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), logRequests(cors(mux))))
}

// ---------------------------------------------------------------------------
// Chain handler
// ---------------------------------------------------------------------------

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"merchant": s.signer.Hex(),
	})
}

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

	// Build query list — only chains where the shop operates.
	var queries []rpcQuery
	if loadedRegistry != nil {
		for _, rc := range loadedRegistry.Chains {
			if rc.RPC == "" {
				continue
			}
			// Only show chains where the shop operates (Ethereum Sepolia for Compound).
			if rc.ChainID != 11155111 {
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

// bridgeToArc bridges USDC to Arc via CCTPv2 with Forwarding Service.
//
// Flow:
//  1. USDC.approve(TokenMessengerV2, amount) on source chain
//  2. TokenMessengerV2.depositForBurn(amount, arcDomain, ...) on source chain
//  3. Poll for forwarding completion (Forwarding Service mints on Arc)
func (s *server) bridgeToArc(chainID int, amountStr string) {
	rc := registryChainByID(chainID)
	if rc == nil || rc.RPC == "" {
		log.Printf("[cctp-bridge] no RPC for chain %d, skipping", chainID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ethClient, err := ethclient.DialContext(ctx, rc.RPC)
	if err != nil {
		log.Printf("[cctp-bridge] dial RPC: %v", err)
		return
	}
	defer ethClient.Close()

	chainIDBig, err := ethClient.ChainID(ctx)
	if err != nil {
		log.Printf("[cctp-bridge] get chain ID: %v", err)
		return
	}

	auth, err := bind.NewKeyedTransactorWithChainID(s.key, chainIDBig)
	if err != nil {
		log.Printf("[cctp-bridge] create transactor: %v", err)
		return
	}
	auth.Context = ctx

	tokenMessenger := merx.TokenMessengerV2
	// Mint directly to the merchant wallet on Arc.
	mintRecipient := addressToBytes32(s.signer)

	erc20ABI, _ := abi.JSON(strings.NewReader(`[{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}]`))
	depositForBurnABI, _ := abi.JSON(strings.NewReader(`[{"type":"function","name":"depositForBurn","inputs":[{"name":"amount","type":"uint256"},{"name":"destinationDomain","type":"uint32"},{"name":"mintRecipient","type":"bytes32"},{"name":"burnToken","type":"address"},{"name":"destinationCaller","type":"bytes32"},{"name":"maxFee","type":"uint256"},{"name":"minFinalityThreshold","type":"uint32"}],"outputs":[]}]`))

	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		log.Printf("[cctp-bridge] invalid amount: %s", amountStr)
		return
	}

	domain, ok := cctpDomainForChain(chainID)
	if !ok {
		log.Printf("[cctp-bridge] no CCTP domain for chain %d", chainID)
		return
	}

	cctpUSDC := common.HexToAddress(usdcAddressForChain(chainID))

	// Step 1: approve TokenMessengerV2 to spend USDC.
	approveData, err := erc20ABI.Pack("approve", tokenMessenger, amount)
	if err != nil {
		log.Printf("[cctp-bridge] pack approve: %v", err)
		return
	}

	approveTx, err := bind.NewBoundContract(cctpUSDC, erc20ABI, ethClient, ethClient, ethClient).RawTransact(auth, approveData)
	if err != nil {
		log.Printf("[cctp-bridge] approve tx: %v", err)
		return
	}
	log.Printf("[cctp-bridge] approve broadcast: %s", approveTx.Hash().Hex())

	receipt, err := bind.WaitMined(ctx, ethClient, approveTx)
	if err != nil {
		log.Printf("[cctp-bridge] approve wait: %v", err)
		return
	}
	if receipt.Status != 1 {
		log.Printf("[cctp-bridge] approve reverted")
		return
	}

	// Step 2: depositForBurn — burns USDC on source chain, CCTP bridges to Arc.
	burnData, err := depositForBurnABI.Pack("depositForBurn",
		amount,
		uint32(26),         // destinationDomain = Arc
		mintRecipient,      // ArcReceiver on Arc
		cctpUSDC,           // burnToken = USDC on source chain
		common.Hash{},      // destinationCaller = permissionless (zero)
		merx.DefaultMaxFee, // maxFee for CCTP forwarding
		uint32(0),          // minFinalityThreshold = 0 (fast transfer)
	)
	if err != nil {
		log.Printf("[cctp-bridge] pack depositForBurn: %v", err)
		return
	}

	auth.Nonce = nil // fresh nonce after approve
	burnTx, err := bind.NewBoundContract(tokenMessenger, depositForBurnABI, ethClient, ethClient, ethClient).RawTransact(auth, burnData)
	if err != nil {
		log.Printf("[cctp-bridge] depositForBurn tx: %v", err)
		return
	}
	log.Printf("[cctp-bridge] depositForBurn broadcast: %s on domain %d", burnTx.Hash().Hex(), domain)

	receipt, err = bind.WaitMined(ctx, ethClient, burnTx)
	if err != nil {
		log.Printf("[cctp-bridge] depositForBurn wait: %v", err)
		return
	}
	if receipt.Status != 1 {
		log.Printf("[cctp-bridge] depositForBurn reverted")
		return
	}

	log.Printf("[cctp-bridge] CCTP burn complete, polling attestation...")

	// Step 3: poll forwarding completion (Forwarding Service mints on Arc).
	s.pollForwardingCompletion("bridge", domain, burnTx.Hash().Hex())
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

// GET /api/pay-tx?chain_id=1301&amount=1000000 — returns the calldata for
// depositForBurnWithHook that the customer executes directly on TokenMessengerV2.
//
// Flow:
//  1. Frontend calls GET /api/pay-tx
//  2. Customer approves USDC spend for TokenMessengerV2
//  3. Customer calls depositForBurnWithHook (CCTP Forwarding Service mints on Arc)
//  4. Frontend sends POST /api/pay with the txHash
func (s *server) handlePayTx(w http.ResponseWriter, r *http.Request) {
	chainIDStr := r.URL.Query().Get("chain_id")
	amountStr := r.URL.Query().Get("amount")

	if chainIDStr == "" || amountStr == "" {
		writeError(w, http.StatusBadRequest, "chain_id and amount are required")
		return
	}

	chainID, err := strconv.Atoi(chainIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chain_id: %s", chainIDStr)
		return
	}

	srcDomain, ok := cctpDomainForChain(chainID)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported chain_id: %d", chainID)
		return
	}

	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", amountStr)
		return
	}

	usdcAddr := usdcAddressForChain(chainID)
	if usdcAddr == "" {
		writeError(w, http.StatusBadRequest, "no USDC address for chain %d", chainID)
		return
	}

	burnToken := common.HexToAddress(usdcAddr)
	mintRecipient := addressToBytes32(s.signer) // shop wallet on Arc
	var zeroCaller [32]byte

	// Estimate forwarding fee for this route.
	maxFee, err := estimateForwardingFee(r.Context(), srcDomain, merx.ArcDomain, amount)
	if err != nil {
		log.Printf("[pay-tx] fee estimation failed, using fallback: %v", err)
		maxFee = merx.ForwardingMaxFee
	}

	calldata, err := s.depositForBurnWithHookABI.Pack("depositForBurnWithHook",
		amount,
		merx.ArcDomain, // destinationDomain = Arc
		mintRecipient,
		burnToken,
		zeroCaller, // destinationCaller = permissionless
		maxFee,
		uint32(0), // minFinalityThreshold
		merx.ForwardingHookData,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack calldata: %v", err)
		return
	}

	writeJSON(w, http.StatusOK, payTxResponse{
		To:      merx.TokenMessengerV2.Hex(),
		Data:    hexutil.Encode(calldata),
		ChainID: chainID,
		Value:   "0",
		MaxFee:  maxFee.String(),
		Approval: approvalData{
			Spender: merx.TokenMessengerV2.Hex(),
			Token:   usdcAddr,
			Amount:  new(big.Int).Add(amount, maxFee).String(), // amount + maxFee (CCTP transfers both)
		},
	})
}

type payTxResponse struct {
	To       string       `json:"to"`
	Data     string       `json:"data"`
	ChainID  int          `json:"chain_id"`
	Value    string       `json:"value"`
	MaxFee   string       `json:"maxFee"`
	Approval approvalData `json:"approval"`
}

type approvalData struct {
	Spender string `json:"spender"`
	Token   string `json:"token"`
	Amount  string `json:"amount"`
}

// POST /api/pay — the customer has already executed depositForBurnWithHook.
// We record the payment and poll for forwarding completion in the background.
func (s *server) handlePay(w http.ResponseWriter, r *http.Request) {
	var req payRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.TxHash == "" || req.ChainID == 0 || req.Amount == "" || req.Owner == "" {
		writeError(w, http.StatusBadRequest, "txHash, chainId, amount, and owner are required")
		return
	}

	sourceDomain, ok := cctpDomainForChain(req.ChainID)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported chainId: %d", req.ChainID)
		return
	}

	// Create an invoice for this payment.
	amountInt, _ := new(big.Int).SetString(req.Amount, 10)
	amountHuman := fmt.Sprintf("%.2f", float64(amountInt.Int64())/1_000_000)
	inv := &Invoice{
		ID:              uuid.New().String(),
		MerchantAddress: s.signer.Hex(),
		ProductID:       req.ProductID,
		PayerAddress:    req.Owner,
		Amount:          req.Amount,
		AmountHuman:     amountHuman,
		ChainID:         req.ChainID,
		Description:     req.Description,
		Status:          "paid",
		TxHash:          req.TxHash,
		CreatedAt:       time.Now(),
	}
	now := time.Now()
	inv.PaidAt = &now
	s.invoices.create(inv)

	log.Printf("payment recorded: tx=%s owner=%s chain=%d amount=%s", req.TxHash, req.Owner, req.ChainID, req.Amount)

	// Background: poll CCTP forwarding service for completion.
	go s.pollForwardingCompletion("pay", sourceDomain, req.TxHash, inv.ID)

	writeJSON(w, http.StatusCreated, payResponse{
		TxHash:    req.TxHash,
		ChainID:   req.ChainID,
		InvoiceID: inv.ID,
	})
}

// pollForwardingCompletion polls the CCTP attestation API until forwarding is
// complete. Updates invoice status along the way if invoiceID is provided.
func (s *server) pollForwardingCompletion(label string, sourceDomain uint32, txHash string, invoiceID ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	invID := ""
	if len(invoiceID) > 0 {
		invID = invoiceID[0]
	}

	if invID != "" {
		s.invoices.updateStatus(invID, "bridging")
	}
	log.Printf("[%s] waiting for forwarding completion: domain=%d tx=%s", label, sourceDomain, txHash)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	attesting := false
	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] timeout waiting for forwarding: tx=%s", label, txHash)
			return
		case <-ticker.C:
		}

		_, _, status, err := s.fetchCCTPAttestation(ctx, sourceDomain, txHash)
		if err != nil {
			log.Printf("[%s] poll error: %v", label, err)
			continue
		}
		if status == "" {
			continue
		}
		if !attesting && status != "complete" && invID != "" {
			s.invoices.updateStatus(invID, "attesting")
			attesting = true
		}
		log.Printf("[%s] status=%s tx=%s", label, status, txHash)
		if status == "complete" {
			if invID != "" {
				s.invoices.setArcTx(invID, txHash)
			}
			log.Printf("[%s] forwarding complete: tx=%s", label, txHash)
			return
		}
	}
}

// pollRefundCompletion polls the CCTP attestation API for a refund and extracts
// the forwarding tx hash when complete.
func (s *server) pollRefundCompletion(invoiceID, arcTxHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	log.Printf("[refund] waiting for forwarding: tx=%s invoice=%s", arcTxHash, invoiceID)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[refund] timeout: tx=%s", arcTxHash)
			return
		case <-ticker.C:
		}

		// Fetch full attestation response to get forwardTxHash.
		url := fmt.Sprintf("%s/%d?transactionHash=%s", merx.CCTPAttestationURL, merx.ArcDomain, arcTxHash)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[refund] poll error: %v", err)
			continue
		}

		var result struct {
			Messages []struct {
				Status        string `json:"status"`
				ForwardState  string `json:"forwardState"`
				ForwardTxHash string `json:"forwardTxHash"`
			} `json:"messages"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		if len(result.Messages) == 0 {
			continue
		}

		m := result.Messages[0]
		log.Printf("[refund] status=%s forwardState=%s tx=%s", m.Status, m.ForwardState, arcTxHash)

		if m.ForwardState == "FAILED" {
			log.Printf("[refund] FAILED: forwarding service rejected tx=%s invoice=%s", arcTxHash, invoiceID)
			return
		}
		if m.ForwardState == "COMPLETE" || m.ForwardState == "CONFIRMED" {
			if invoiceID != "" {
				if m.ForwardTxHash != "" {
					s.invoices.setRefundTx(invoiceID, m.ForwardTxHash)
				} else {
					s.invoices.setRefundTx(invoiceID, arcTxHash)
				}
			}
			log.Printf("[refund] complete: forwardState=%s forwardTx=%s invoice=%s", m.ForwardState, m.ForwardTxHash, invoiceID)
			return
		}
	}
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

type payRequest struct {
	TxHash      string `json:"txHash"`
	ChainID     int    `json:"chainId"`
	Amount      string `json:"amount"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	ProductID   string `json:"productId"`
}

type payResponse struct {
	TxHash    string `json:"txHash"`
	ChainID   int    `json:"chainId"`
	InvoiceID string `json:"invoiceId"`
}

type refundRequest struct {
	InvoiceID string `json:"invoiceId"`
	To        string `json:"to"`
	ChainID   int    `json:"chainId"`
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

	dstDomain, ok := cctpDomainForChain(req.ChainID)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported chainId: %d", req.ChainID)
		return
	}

	recipient := common.HexToAddress(req.To)
	ctx := r.Context()

	// Estimate forwarding fee for this route.
	maxFee, err := estimateForwardingFee(ctx, merx.ArcDomain, dstDomain, amount)
	if err != nil {
		log.Printf("[refund] fee estimation failed, using fallback: %v", err)
		maxFee = merx.ForwardingMaxFee
	}

	// Send amount + fee so the customer receives the full amount.
	burnAmount := new(big.Int).Add(amount, maxFee)

	// Check shop USDC balance on Arc.
	arcBalance, err := s.getArcUSDCBalance(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "check balance: %v", err)
		return
	}
	if arcBalance.Cmp(burnAmount) < 0 {
		writeError(w, http.StatusConflict, "insufficient USDC on Arc: have %s, need %s (includes %s fee)", arcBalance, burnAmount, maxFee)
		return
	}

	// depositForBurnWithHook on Arc → customer's chain via Forwarding Service.
	mintRecipient := addressToBytes32(recipient)
	burnToken := merx.TestnetUSDC[merx.ArcDomain]
	var zeroCaller [32]byte

	calldata, err := s.depositForBurnWithHookABI.Pack("depositForBurnWithHook",
		burnAmount,
		dstDomain,
		mintRecipient,
		burnToken,
		zeroCaller,
		maxFee,
		uint32(0), // minFinalityThreshold
		merx.ForwardingHookData,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pack depositForBurnWithHook: %v", err)
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

	contract := bind.NewBoundContract(merx.TokenMessengerV2, s.depositForBurnWithHookABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		writeError(w, http.StatusBadGateway, "broadcast depositForBurnWithHook: %v", err)
		return
	}

	txHash := tx.Hash().Hex()
	log.Printf("refund started: tx=%s to=%s chain=%d amount=%s invoice=%s", txHash, req.To, req.ChainID, req.Amount, req.InvoiceID)

	// Mark invoice as refunded with the Arc burn tx.
	if req.InvoiceID != "" {
		s.invoices.setRefundArcTx(req.InvoiceID, txHash, int(req.ChainID))
	}

	// Background: poll CCTP forwarding completion (no self-relay needed).
	go s.pollRefundCompletion(req.InvoiceID, txHash)

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

	// Background: poll CCTP forwarding completion (no self-relay needed).
	go s.pollForwardingCompletion("withdraw", 0, txHash) // domain 0 = Eth Sepolia

	writeJSON(w, http.StatusCreated, map[string]string{"txHash": txHash})
}

// estimateForwardingFee calls the CCTP fee API and returns the recommended maxFee
// (protocolFee + forwardFee at "med" level) for a given route and amount.
func estimateForwardingFee(ctx context.Context, sourceDomain, destDomain uint32, amount *big.Int) (*big.Int, error) {
	url := fmt.Sprintf("https://iris-api-sandbox.circle.com/v2/burn/USDC/fees/%d/%d?forward=true", sourceDomain, destDomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fee API: %w", err)
	}
	defer resp.Body.Close()

	var fees []struct {
		FinalityThreshold int     `json:"finalityThreshold"`
		MinimumFee        float64 `json:"minimumFee"` // bps
		ForwardFee        struct {
			Med int64 `json:"med"`
		} `json:"forwardFee"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fees); err != nil {
		return nil, fmt.Errorf("parse fee response: %w", err)
	}

	if len(fees) == 0 {
		return merx.ForwardingMaxFee, nil // fallback
	}

	// Use the first entry (fast transfer, finalityThreshold=1000).
	fee := fees[0]

	// protocolFee = amount * minimumFee(bps) / 10000
	protocolFee := new(big.Int).Mul(amount, big.NewInt(int64(fee.MinimumFee*100)))
	protocolFee.Div(protocolFee, big.NewInt(1_000_000))

	// maxFee = protocolFee + forwardFee(med)
	maxFee := new(big.Int).Add(protocolFee, big.NewInt(fee.ForwardFee.Med))

	// Add 10% margin
	margin := new(big.Int).Div(maxFee, big.NewInt(10))
	maxFee.Add(maxFee, margin)

	return maxFee, nil
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
