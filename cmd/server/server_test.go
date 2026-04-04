package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	merx "github.com/RomainLafont/merx"
	"github.com/RomainLafont/merx/gateway"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("set INTEGRATION=1 to run live API tests")
	}
}

func setupServer(t *testing.T) *server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := gateway.NewClient(gateway.Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.GetInfo(t.Context())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	paymasterABI, err := abi.JSON(strings.NewReader(payWithPermitABIJSON))
	if err != nil {
		t.Fatalf("parse paymaster ABI: %v", err)
	}
	arcReceiverABI, err := abi.JSON(strings.NewReader(relayAndDepositABIJSON))
	if err != nil {
		t.Fatalf("parse flush ABI: %v", err)
	}
	return &server{client: client, info: info, paymasterABI: paymasterABI, arcReceiverABI: arcReceiverABI}
}

func TestHandleInfo(t *testing.T) {
	skipUnlessIntegration(t)
	s := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	w := httptest.NewRecorder()
	s.handleInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp gateway.InfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Version < 1 {
		t.Fatalf("unexpected version: %d", resp.Version)
	}
	if len(resp.Domains) == 0 {
		t.Fatal("no domains")
	}
	t.Logf("domains: %d", len(resp.Domains))
}

func TestHandleBalances(t *testing.T) {
	skipUnlessIntegration(t)
	s := setupServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/balances", nil)
	w := httptest.NewRecorder()
	s.handleBalances(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp gateway.BalancesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Balances) == 0 {
		t.Fatal("no balances returned")
	}
	t.Logf("balances: %d entries", len(resp.Balances))
}

// setupServerOffline creates a server without calling the live Gateway API.
// Sufficient for handlers that don't need Gateway info (e.g. pay-tx).
func setupServerOffline(t *testing.T) *server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := gateway.NewClient(gateway.Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	paymasterABI, err := abi.JSON(strings.NewReader(payWithPermitABIJSON))
	if err != nil {
		t.Fatalf("parse paymaster ABI: %v", err)
	}
	arcReceiverABI, err := abi.JSON(strings.NewReader(relayAndDepositABIJSON))
	if err != nil {
		t.Fatalf("parse flush ABI: %v", err)
	}
	return &server{client: client, paymasterABI: paymasterABI, arcReceiverABI: arcReceiverABI}
}

// fakePaymaster is a dummy address used in tests.
var fakePaymaster = common.HexToAddress("0x1111111111111111111111111111111111111111")

func TestHandlePayTx(t *testing.T) {
	// Temporarily register a fake paymaster for Unichain Sepolia.
	merx.ShopPaymaster[1301] = fakePaymaster
	defer delete(merx.ShopPaymaster, 1301)

	s := setupServerOffline(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pay-tx?chain_id=1301&amount=1000000", nil)
	w := httptest.NewRecorder()
	s.handlePayTx(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var resp payTxResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.ChainID != 1301 {
		t.Fatalf("unexpected chain_id: %d", resp.ChainID)
	}
	if resp.Amount != "1000000" {
		t.Fatalf("unexpected amount: %s", resp.Amount)
	}
	if resp.Deadline == "" {
		t.Fatal("expected deadline")
	}
	if resp.Permit.Spender != fakePaymaster.Hex() {
		t.Fatalf("unexpected permit spender: %s", resp.Permit.Spender)
	}
	if resp.Permit.Domain.Name != "USDC" {
		t.Fatalf("unexpected domain name: %s", resp.Permit.Domain.Name)
	}
	if resp.Permit.Domain.ChainID != 1301 {
		t.Fatalf("unexpected domain chain_id: %d", resp.Permit.Domain.ChainID)
	}
}

func TestHandlePayTx_Validation(t *testing.T) {
	s := setupServerOffline(t)

	tests := []struct {
		name  string
		query string
		code  int
	}{
		{"missing chain_id", "amount=1000000", http.StatusBadRequest},
		{"missing amount", "chain_id=1301", http.StatusBadRequest},
		{"invalid chain_id", "chain_id=abc&amount=1000000", http.StatusBadRequest},
		{"unsupported chain", "chain_id=999&amount=1000000", http.StatusBadRequest},
		{"invalid amount", "chain_id=1301&amount=abc", http.StatusBadRequest},
		{"zero amount", "chain_id=1301&amount=0", http.StatusBadRequest},
		{"negative amount", "chain_id=1301&amount=-1", http.StatusBadRequest},
		{"no paymaster deployed", "chain_id=1301&amount=1000000", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/pay-tx?"+tt.query, nil)
			w := httptest.NewRecorder()
			s.handlePayTx(w, req)

			if w.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleRefund_Validation(t *testing.T) {
	skipUnlessIntegration(t)
	s := setupServer(t)

	tests := []struct {
		name string
		body string
		code int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"missing to", `{"chain":10,"amount":"1000000"}`, http.StatusBadRequest},
		{"missing chain", `{"to":"0xdead","amount":"1000000"}`, http.StatusBadRequest},
		{"missing amount", `{"to":"0xdead","chain":10}`, http.StatusBadRequest},
		{"invalid amount", `{"to":"0xdead","chain":10,"amount":"abc"}`, http.StatusBadRequest},
		{"zero amount", `{"to":"0xdead","chain":10,"amount":"0"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleRefund(w, req)

			if w.Code != tt.code {
				t.Fatalf("expected %d, got %d: %s", tt.code, w.Code, w.Body.String())
			}

			var resp errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Error == "" {
				t.Fatal("expected error message")
			}
			t.Logf("error: %s", resp.Error)
		})
	}
}

func TestHandleRefund_InsufficientBalance(t *testing.T) {
	skipUnlessIntegration(t)
	s := setupServer(t)

	// Random key has no Gateway balance — should return 409 Conflict.
	body := `{"to":"0xdead","chain":10,"amount":"999999999999"}`
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleRefund(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleRefundStatus_BadID(t *testing.T) {
	skipUnlessIntegration(t)
	s := setupServer(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/refund/{id}", s.handleRefundStatus)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/refund/nonexistent-id")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Gateway API returns an error for unknown transfer IDs.
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected error for nonexistent transfer ID")
	}
	t.Logf("status: %d", resp.StatusCode)
}

func TestCORS(t *testing.T) {
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/info", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", w.Code)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("missing CORS origin header")
		}
	})

	t.Run("normal request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/info", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("missing CORS origin header on normal request")
		}
	})
}
