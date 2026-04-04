// Local API server exposing Gateway operations for the frontend.
//
// Usage:
//
//	PRIVATE_KEY=0x... go run cmd/server/main.go
//	PRIVATE_KEY=0x... go run cmd/server/main.go --port 3001
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	merx "github.com/RomainLafont/merx"
	"github.com/RomainLafont/merx/gateway"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const payWithPermitABIJSON = `[{"type":"function","name":"payWithPermit","inputs":[{"name":"owner","type":"address"},{"name":"amount","type":"uint256"},{"name":"deadline","type":"uint256"},{"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"},{"name":"maxFee","type":"uint256"}],"outputs":[]}]`

const relayAndDepositABIJSON = `[{"type":"function","name":"relayAndDeposit","inputs":[{"name":"message","type":"bytes"},{"name":"attestation","type":"bytes"},{"name":"depositor","type":"address"}],"outputs":[]}]`

type server struct {
	client          *gateway.Client
	info            *gateway.InfoResponse
	paymasterABI    abi.ABI
	arcReceiverABI  abi.ABI
}

func main() {
	port := flag.Int("port", 8080, "HTTP port")
	privKeyHex := flag.String("private-key", "", "hex private key (or PRIVATE_KEY env)")
	flag.Parse()

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

	client, err := gateway.NewClient(gateway.Config{PrivateKey: key})
	if err != nil {
		log.Fatal(err)
	}

	// Pre-fetch info at startup.
	info, err := client.GetInfo(context.Background())
	if err != nil {
		log.Fatalf("GetInfo: %v", err)
	}

	paymasterABI, err := abi.JSON(strings.NewReader(payWithPermitABIJSON))
	if err != nil {
		log.Fatalf("parse paymaster ABI: %v", err)
	}
	arcReceiverABI, err := abi.JSON(strings.NewReader(relayAndDepositABIJSON))
	if err != nil {
		log.Fatalf("parse merx.ArcReceiver ABI: %v", err)
	}

	s := &server{client: client, info: info, paymasterABI: paymasterABI, arcReceiverABI: arcReceiverABI}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/balances", s.handleBalances)
	mux.HandleFunc("GET /api/pay-tx", s.handlePayTx)
	mux.HandleFunc("POST /api/pay", s.handlePay)
	mux.HandleFunc("POST /api/refund", s.handleRefund)
	mux.HandleFunc("GET /api/refund/{id}", s.handleRefundStatus)

	log.Printf("signer: %s", client.SignerAddress())
	log.Printf("listening on :%d", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), logRequests(cors(mux))))
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GET /api/info — Gateway domains, contracts, processed heights.
func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	// Refresh info on each call.
	info, err := s.client.GetInfo(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "GetInfo: %v", err)
		return
	}
	s.info = info
	writeJSON(w, http.StatusOK, info)
}

// GET /api/balances — shop's Gateway balances across all domains.
func (s *server) handleBalances(w http.ResponseWriter, r *http.Request) {
	bal, err := s.client.GetBalances(r.Context(), &gateway.BalancesRequest{
		Token:   "USDC",
		Sources: []gateway.BalanceSource{{Depositor: s.client.SignerAddress().Hex()}},
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "GetBalances: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, bal)
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

	auth, err := bind.NewKeyedTransactorWithChainID(s.client.Key(), chainIDBig)
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
	log.Printf("payment broadcast: tx=%s owner=%s chain=%d amount=%s", txHash, req.Owner, req.ChainID, req.Amount)

	// Background: poll CCTP attestation, then self-relay on Arc.
	go s.pollAndRelay(sourceDomain, txHash)

	writeJSON(w, http.StatusCreated, payResponse{
		TxHash:  txHash,
		ChainID: req.ChainID,
	})
}

// pollAndRelay waits for the CCTP attestation, then self-relays on Arc
// (receiveMessage + depositFor in one tx via ArcReceiver).
func (s *server) pollAndRelay(sourceDomain uint32, txHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	log.Printf("[relay] waiting for CCTP attestation: domain=%d tx=%s", sourceDomain, txHash)

	// Poll CCTP attestation API until status = complete.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var message, attestation string
	for {
		select {
		case <-ctx.Done():
			log.Printf("[relay] timeout waiting for attestation: tx=%s", txHash)
			return
		case <-ticker.C:
		}

		msg, att, status, err := s.getCCTPAttestation(ctx, sourceDomain, txHash)
		if err != nil {
			log.Printf("[relay] poll error: %v", err)
			continue
		}
		if status == "" {
			continue // not found yet
		}
		log.Printf("[relay] status: %s", status)
		if status == "complete" {
			message = msg
			attestation = att
			break
		}
	}

	// Self-relay on Arc: receiveMessage + depositFor in one tx.
	if err := s.relayOnArc(ctx, message, attestation); err != nil {
		log.Printf("[relay] ERROR: %v", err)
		return
	}
	log.Printf("[relay] deposit into Gateway complete: tx=%s", txHash)
}

// getCCTPAttestation polls the CCTP attestation API and returns (message, attestation, status).
func (s *server) getCCTPAttestation(ctx context.Context, sourceDomain uint32, txHash string) (string, string, string, error) {
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

// relayOnArc calls ArcReceiver.relayAndDeposit(message, attestation, shopAddress) on Arc.
func (s *server) relayOnArc(ctx context.Context, message, attestation string) error {
	msgBytes, err := hexutil.Decode(message)
	if err != nil {
		return fmt.Errorf("decode message: %w", err)
	}
	attBytes, err := hexutil.Decode(attestation)
	if err != nil {
		return fmt.Errorf("decode attestation: %w", err)
	}

	calldata, err := s.arcReceiverABI.Pack("relayAndDeposit", msgBytes, attBytes, s.client.SignerAddress())
	if err != nil {
		return fmt.Errorf("pack relayAndDeposit: %w", err)
	}

	ethClient, err := ethclient.DialContext(ctx, merx.ArcRPCURL)
	if err != nil {
		return fmt.Errorf("dial Arc RPC: %w", err)
	}
	defer ethClient.Close()

	auth, err := bind.NewKeyedTransactorWithChainID(s.client.Key(), big.NewInt(merx.ArcChainID))
	if err != nil {
		return fmt.Errorf("create transactor: %w", err)
	}
	auth.Context = ctx

	contract := bind.NewBoundContract(merx.ArcReceiver, s.arcReceiverABI, ethClient, ethClient, ethClient)
	tx, err := contract.RawTransact(auth, calldata)
	if err != nil {
		return fmt.Errorf("broadcast relayAndDeposit: %w", err)
	}

	log.Printf("[relay] Arc tx broadcast: %s", tx.Hash().Hex())
	return nil
}

type payRequest struct {
	Owner     string `json:"owner"`
	ChainID   uint64 `json:"chain_id"`
	Amount    string `json:"amount"`
	Deadline  string `json:"deadline"`
	Signature string `json:"signature"` // 0x-prefixed hex, 65 bytes (r + s + v)
}

type payResponse struct {
	TxHash  string `json:"tx_hash"`
	ChainID uint64 `json:"chain_id"`
}

type refundRequest struct {
	To     string `json:"to"`
	Chain  uint32 `json:"chain"`
	Amount string `json:"amount"`
}

type refundResponse struct {
	TransferID string       `json:"transferId"`
	Fees       gateway.Fees `json:"fees"`
}

// POST /api/refund — start a refund. Returns immediately with a transferId.
// The frontend polls GET /api/refund/{id} for status.
func (s *server) handleRefund(w http.ResponseWriter, r *http.Request) {
	var req refundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}

	if req.To == "" || req.Chain == 0 || req.Amount == "" {
		writeError(w, http.StatusBadRequest, "to, chain, and amount are required")
		return
	}

	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, "invalid amount: %s", req.Amount)
		return
	}

	recipient := common.HexToAddress(req.To)
	ctx := r.Context()

	// Balances.
	bal, err := s.client.GetBalances(ctx, &gateway.BalancesRequest{
		Token:   "USDC",
		Sources: []gateway.BalanceSource{{Depositor: s.client.SignerAddress().Hex()}},
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "GetBalances: %v", err)
		return
	}

	// Allocate sources.
	allocs := gateway.AllocateBalances(bal.Balances, amount, req.Chain, -1)
	if allocs == nil {
		writeError(w, http.StatusConflict, "insufficient Gateway balance for %s USDC", amount)
		return
	}

	dstDomain := s.info.LookupDomain(req.Chain)
	if dstDomain == nil {
		writeError(w, http.StatusBadRequest, "destination domain %d not found", req.Chain)
		return
	}

	dstUSDC, ok := merx.TestnetUSDC[req.Chain]
	if !ok {
		writeError(w, http.StatusBadRequest, "no known USDC address for domain %d", req.Chain)
		return
	}

	// Build intents.
	var intents []gateway.BurnIntent
	for _, a := range allocs {
		srcDomain := s.info.LookupDomain(a.Domain)
		if srcDomain == nil {
			writeError(w, http.StatusInternalServerError, "source domain %d not found", a.Domain)
			return
		}
		srcUSDC, ok := merx.TestnetUSDC[a.Domain]
		if !ok {
			writeError(w, http.StatusInternalServerError, "no USDC address for domain %d", a.Domain)
			return
		}

		spec, err := s.client.BuildTransferSpec(gateway.TransferSpecParams{
			SourceDomain:      a.Domain,
			DestinationDomain: req.Chain,
			SourceWallet:      common.HexToAddress(srcDomain.WalletContract.Address),
			DestinationMinter: common.HexToAddress(dstDomain.MinterContract.Address),
			SourceToken:       srcUSDC,
			DestinationToken:  dstUSDC,
			Depositor:         s.client.SignerAddress(),
			Recipient:         recipient,
			Value:             a.Amount,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "BuildTransferSpec: %v", err)
			return
		}
		intents = append(intents, gateway.BurnIntent{Spec: *spec})
	}

	// Estimate (no forwarding — self-relay).
	est, err := s.client.Estimate(ctx, intents, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Estimate: %v", err)
		return
	}

	// Sign each filled intent.
	var signed []gateway.SignedBurnIntentRequest
	for i := range est.Body {
		filled := est.Body[i].BurnIntent
		sig, err := s.client.SignBurnIntent(&filled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SignBurnIntent: %v", err)
			return
		}
		signed = append(signed, gateway.SignedBurnIntentRequest{
			BurnIntent: &filled,
			Signature:  hexutil.Encode(sig),
		})
	}

	// Transfer (no forwarding — self-relay).
	resp, err := s.client.Transfer(ctx, signed, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Transfer: %v", err)
		return
	}

	log.Printf("refund started: id=%s to=%s chain=%d amount=%s", resp.TransferID, req.To, req.Chain, req.Amount)

	// Resolve destination RPC for self-relay mint.
	dstChainID := domainToChainID(req.Chain)
	dstRPC := merx.RPCURLs[dstChainID]
	minterAddr := common.HexToAddress(dstDomain.MinterContract.Address)

	// Background: poll for attestation, then self-relay gatewayMint on destination.
	go s.pollAndMint(resp.TransferID, dstRPC, minterAddr)

	writeJSON(w, http.StatusCreated, refundResponse{
		TransferID: resp.TransferID,
		Fees:       resp.Fees,
	})
}

// GET /api/refund/{id} — poll refund status.
func (s *server) handleRefundStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing transfer id")
		return
	}

	status, err := s.client.GetTransferStatus(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "GetTransferStatus: %v", err)
		return
	}

	writeJSON(w, http.StatusOK, status)
}

// pollAndMint polls Gateway transfer status until the attestation is available,
// then self-relays gatewayMint on the destination chain.
func (s *server) pollAndMint(transferID, rpcURL string, minterAddr common.Address) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	log.Printf("[refund-relay] waiting for attestation: id=%s", transferID)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[refund-relay] timeout: id=%s", transferID)
			return
		case <-ticker.C:
		}

		status, err := s.client.GetTransferStatus(ctx, transferID)
		if err != nil {
			log.Printf("[refund-relay] poll error: %v", err)
			continue
		}

		if status.Attestation == nil {
			continue
		}

		log.Printf("[refund-relay] attestation ready, submitting gatewayMint")

		attPayload, err := hexutil.Decode(status.Attestation.Payload)
		if err != nil {
			log.Printf("[refund-relay] decode attestation payload: %v", err)
			return
		}
		attSig, err := hexutil.Decode(status.Attestation.Signature)
		if err != nil {
			log.Printf("[refund-relay] decode attestation signature: %v", err)
			return
		}

		txHash, err := s.client.SubmitMint(ctx, rpcURL, minterAddr, attPayload, attSig)
		if err != nil {
			log.Printf("[refund-relay] gatewayMint failed: %v", err)
			return
		}

		log.Printf("[refund-relay] gatewayMint broadcast: tx=%s id=%s", txHash.Hex(), transferID)
		return
	}
}

// domainToChainID returns the EVM chain ID for a CCTP domain.
func domainToChainID(domain uint32) uint64 {
	for chainID, d := range merx.ChainIDToDomain {
		if d == domain {
			return chainID
		}
	}
	return 0
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

