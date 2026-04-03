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
	"strings"
	"time"
	"github.com/RomainLafont/merx/gateway"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Testnet-only key. Address: 0x3338A40C3362e6974AA2feCC06a536FF73D6797d
const defaultPrivateKey = "63de9a8de555c9e160c577087e4d43865f6018aeb5bf919268ed5de5d525a126"
const defaultAddress = "0x3338A40C3362e6974AA2feCC06a536FF73D6797d"

var testnetUSDC = map[uint32]common.Address{
	0:  common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"),
	1:  common.HexToAddress("0x5425890298aed601595a70AB815c96711a31Bc65"),
	6:  common.HexToAddress("0x036CbD53842c5426634e7929541eC2318f3dCF7e"),
	10: common.HexToAddress("0x31d0220469e10c4E71834a79b1f276d740d3768F"),
}

type server struct {
	client *gateway.Client
	info   *gateway.InfoResponse
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
		keyHex = defaultPrivateKey
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

	s := &server{client: client, info: info}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/balances", s.handleBalances)
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

	dstUSDC, ok := testnetUSDC[req.Chain]
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
		srcUSDC, ok := testnetUSDC[a.Domain]
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

	// Estimate.
	est, err := s.client.Estimate(ctx, intents, &gateway.EstimateOptions{EnableForwarder: true})
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

	// Transfer.
	resp, err := s.client.Transfer(ctx, signed, &gateway.TransferOptions{EnableForwarder: true})
	if err != nil {
		writeError(w, http.StatusBadGateway, "Transfer: %v", err)
		return
	}

	log.Printf("refund started: id=%s to=%s chain=%d amount=%s", resp.TransferID, req.To, req.Chain, req.Amount)
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

