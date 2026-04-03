package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/RomainLafont/merx/gateway"
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
	return &server{client: client, info: info}
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
