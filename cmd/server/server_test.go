package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/crypto"
)

// setupServerOffline creates a server without calling any live APIs.
func setupServerOffline(t *testing.T) *server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	depositForBurnABI, err := abi.JSON(strings.NewReader(depositForBurnABIJSON))
	if err != nil {
		t.Fatalf("parse depositForBurn ABI: %v", err)
	}
	depositForBurnWithHookABI, err := abi.JSON(strings.NewReader(depositForBurnWithHookABIJSON))
	if err != nil {
		t.Fatalf("parse depositForBurnWithHook ABI: %v", err)
	}
	relayAndSupplyABI, err := abi.JSON(strings.NewReader(relayAndSupplyABIJSON))
	if err != nil {
		t.Fatalf("parse relayAndSupply ABI: %v", err)
	}
	return &server{
		key:                       key,
		signer:                    signer,
		depositForBurnABI:         depositForBurnABI,
		depositForBurnWithHookABI: depositForBurnWithHookABI,
		relayAndSupplyABI:         relayAndSupplyABI,
		invoices:                  newInvoiceStore(filepath.Join(t.TempDir(), "invoices.json")),
	}
}

func TestHandlePayTx(t *testing.T) {
	// Load a minimal registry for the test.
	loadedRegistry = &registry{
		Chains: []registryChain{
			{
				Name:       "Unichain Sepolia",
				ChainID:    1301,
				CCTPDomain: 10,
				Tokens: []tokenEntry{
					{Symbol: "USDC", Decimals: 6, Address: "0x31d0220469e10c4E71834a79b1f276d740d3768F"},
				},
			},
		},
	}
	defer func() { loadedRegistry = nil }()

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
	if resp.To == "" {
		t.Fatal("expected to address")
	}
	if resp.Data == "" {
		t.Fatal("expected calldata")
	}
	if resp.Value != "0" {
		t.Fatalf("unexpected value: %s", resp.Value)
	}
	if resp.Approval.Amount != "1000000" {
		t.Fatalf("unexpected approval amount: %s", resp.Approval.Amount)
	}
	if resp.Approval.Spender == "" {
		t.Fatal("expected approval spender")
	}
}

func TestHandlePayTx_Validation(t *testing.T) {
	// No registry loaded — all chains unsupported.
	loadedRegistry = nil

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
	s := setupServerOffline(t)

	tests := []struct {
		name string
		body string
		code int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"missing to", `{"chainId":1301,"amount":"1000000"}`, http.StatusBadRequest},
		{"missing chainId", `{"to":"0xdead","amount":"1000000"}`, http.StatusBadRequest},
		{"missing amount", `{"to":"0xdead","chainId":1301}`, http.StatusBadRequest},
		{"invalid amount", `{"to":"0xdead","chainId":1301,"amount":"abc"}`, http.StatusBadRequest},
		{"zero amount", `{"to":"0xdead","chainId":1301,"amount":"0"}`, http.StatusBadRequest},
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

func TestCORS(t *testing.T) {
	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/api/balances", nil)
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
		req := httptest.NewRequest(http.MethodGet, "/api/balances", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("missing CORS origin header on normal request")
		}
	})
}
