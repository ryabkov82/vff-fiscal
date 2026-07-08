package lknpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/state"
)

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
		if body["paymentType"] != "ACCOUNT" {
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
		PaymentType:    "ACCOUNT",
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
