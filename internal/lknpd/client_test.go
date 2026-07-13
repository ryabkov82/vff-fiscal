package lknpd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func TestAPIErrorErrorExcludesUpstreamBody(t *testing.T) {
	const secretBody = "FNS_RAW_BODY_SECRET inn=123456789012 token=SECRET_TOKEN"

	t.Run("http status hides body", func(t *testing.T) {
		err := &APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: secretBody}
		message := err.Error()
		if strings.Contains(message, secretBody) || strings.Contains(message, "SECRET_TOKEN") || strings.Contains(message, "123456789012") {
			t.Fatalf("Error() leaked upstream body: %q", message)
		}
		if !strings.Contains(message, "POST /income") || !strings.Contains(message, "400") {
			t.Fatalf("Error() must report operation and status: %q", message)
		}
	})

	t.Run("body retained for diagnostics only", func(t *testing.T) {
		err := &APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: secretBody}
		if err.Body != secretBody {
			t.Fatal("Body field must be preserved for in-process diagnostics")
		}
	})

	t.Run("transport error text is not exposed but stays programmatic", func(t *testing.T) {
		transportErr := errors.New("dial tcp SECRET_HOST:443: refused TRANSPORT_SECRET token=SECRET_TOKEN inn=123456789012 https://lknpd.nalog.ru/api")
		err := &APIError{Operation: "POST /income", Err: transportErr}
		message := err.Error()

		if message != "POST /income: upstream request failed" {
			t.Fatalf("transport error must render as a safe static message, got %q", message)
		}
		for _, leak := range []string{"SECRET_HOST", "TRANSPORT_SECRET", "SECRET_TOKEN", "123456789012", "https://", "dial tcp"} {
			if strings.Contains(message, leak) {
				t.Fatalf("Error() leaked %q via transport error: %q", leak, message)
			}
		}
		// Unwrap keeps the wrapped error available programmatically.
		if !errors.Is(err, transportErr) {
			t.Fatal("APIError must unwrap to its wrapped transport error")
		}
	})

	t.Run("sentinel stays programmatic but out of the string", func(t *testing.T) {
		err := &APIError{Operation: "POST /income", Err: ErrEmptyApprovedReceiptUUID}
		message := err.Error()
		if message != "POST /income: upstream request failed" {
			t.Fatalf("sentinel error must render as a safe static message, got %q", message)
		}
		if strings.Contains(message, ErrEmptyApprovedReceiptUUID.Error()) {
			t.Fatalf("Error() leaked sentinel text: %q", message)
		}
		if !errors.Is(err, ErrEmptyApprovedReceiptUUID) {
			t.Fatal("APIError must unwrap to its sentinel error")
		}
	})

	t.Run("transport error with body and status still hides everything", func(t *testing.T) {
		err := &APIError{Operation: "POST /income", Status: http.StatusInternalServerError, Body: secretBody, Err: errors.New("TRANSPORT_SECRET")}
		message := err.Error()
		for _, leak := range []string{secretBody, "SECRET_TOKEN", "123456789012", "TRANSPORT_SECRET"} {
			if strings.Contains(message, leak) {
				t.Fatalf("Error() leaked %q on status+err: %q", leak, message)
			}
		}
	})
}

func TestCreateIncomeRefreshesTokenAndPersistsRotation(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["refreshToken"] != "refresh-old" {
			t.Fatalf("unexpected refresh token: %#v", body["refreshToken"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":         "access-1",
			"refreshToken":  "refresh-new",
			"tokenExpireIn": time.Now().Add(time.Hour).Format(time.RFC3339),
			"profile":       map[string]any{"inn": "123456789012"},
		})
	})
	mux.HandleFunc("/income", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-1" {
			t.Fatalf("unexpected authorization: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["paymentType"] != "WIRE" {
			t.Fatalf("unexpected payment type: %#v", body["paymentType"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"approvedReceiptUuid": "receipt-1"})
	})

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{
		RefreshToken: "refresh-old",
		DeviceID:     "device-1",
		INN:          "123456789012",
	})
	if err != nil {
		t.Fatal(err)
	}

	client := New(Config{
		BaseURL:        server.URL,
		UserAgent:      "test-agent",
		TimezoneOffset: "+03:00",
		PaymentType:    "WIRE",
		Timeout:        2 * time.Second,
	}, store)

	receipt, err := client.CreateIncome(context.Background(), CreateIncomeParams{
		Amount:        "299.00",
		ServiceName:   "VPN",
		OperationTime: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReceiptUUID != "receipt-1" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if got := store.Auth().RefreshToken; got != "refresh-new" {
		t.Fatalf("rotated refresh token was not persisted: %q", got)
	}
}

func TestCreateIncomeEmptyApprovedUUIDIsAmbiguous(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":         "access-1",
			"tokenExpireIn": time.Now().Add(time.Hour).Format(time.RFC3339),
			"profile":       map[string]any{"inn": "123456789012"},
		})
	})
	mux.HandleFunc("/income", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"approvedReceiptUuid": ""})
	})

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{
		RefreshToken: "refresh-old",
		DeviceID:     "device-1",
		INN:          "123456789012",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Config{
		BaseURL:        server.URL,
		UserAgent:      "test-agent",
		TimezoneOffset: "+03:00",
		PaymentType:    "WIRE",
		Timeout:        2 * time.Second,
	}, store)

	_, err = client.CreateIncome(context.Background(), CreateIncomeParams{Amount: "10.00", ServiceName: "VPN", OperationTime: time.Now()})
	if !errors.Is(err, ErrEmptyApprovedReceiptUUID) {
		t.Fatalf("expected ErrEmptyApprovedReceiptUUID, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Ambiguous {
		t.Fatalf("expected ambiguous APIError, got %v", err)
	}
}

func TestFormatTimeConvertsToConfiguredOffset(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Config{TimezoneOffset: "+03:00", Timeout: time.Second}, store)

	got := client.formatTime(time.Date(2026, 7, 9, 16, 30, 0, 0, time.UTC))
	if got != "2026-07-09T19:30:00+03:00" {
		t.Fatalf("unexpected formatted time: %q", got)
	}
}
