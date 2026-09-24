package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func postCancel(t *testing.T, server *httptest.Server, externalID string, payload map[string]string) *http.Response {
	t.Helper()
	body := []byte("{}")
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = encoded
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/receipts/"+externalID+"/cancel", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-api-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func seedCreatedReceipt(t *testing.T, server *httptest.Server, externalID string) {
	t.Helper()
	response := postReceipt(t, server, externalID, true)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("seed create: expected 201, got %d", response.StatusCode)
	}
}

func TestCancelCreatedToCancelled(t *testing.T) {
	fake := &fakeLKNPD{}
	server, store := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:150")

	response := postCancel(t, server, "shm:150", map[string]string{
		"comment":        "Возврат средств",
		"operation_time": "2026-09-24T12:00:00Z",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	record := decodeReceipt(t, response)
	if record.Status != receiptStatusCancelled {
		t.Fatalf("status = %s", record.Status)
	}
	if fake.cancelIncomeCalls.Load() != 1 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
	stored, ok := store.GetReceipt("shm:150")
	if !ok || stored.Status != receiptStatusCancelled || stored.LastError != "" {
		t.Fatalf("stored = %+v ok=%v", stored, ok)
	}
}

func TestCancelDuplicateCancelledSkipsUpstream(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:dup-cancel")

	first := postCancel(t, server, "shm:dup-cancel", nil)
	first.Body.Close()
	second := postCancel(t, server, "shm:dup-cancel", nil)
	second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", second.StatusCode)
	}
	if fake.cancelIncomeCalls.Load() != 1 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
}

func TestCancelConcurrentOneUpstreamCall(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeLKNPD{}
	var once sync.Once
	fake.onCancelIncome = func() {
		once.Do(func() { close(started) })
		<-release
	}
	server, store := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:race")

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			response := postCancel(t, server, "shm:race", nil)
			codes[i] = response.StatusCode
			response.Body.Close()
		}(i)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelIncome was not called")
	}
	close(release)
	wg.Wait()

	if fake.cancelIncomeCalls.Load() != 1 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
	stored, ok := store.GetReceipt("shm:race")
	if !ok || stored.Status != receiptStatusCancelled {
		t.Fatalf("stored = %+v ok=%v", stored, ok)
	}
	okCount := 0
	for _, code := range codes {
		if code == http.StatusOK || code == http.StatusConflict {
			okCount++
		}
	}
	if okCount != 2 {
		t.Fatalf("codes = %v", codes)
	}
}

func TestCancelDefiniteUpstreamErrorReturnsToCreated(t *testing.T) {
	fake := &fakeLKNPD{
		cancelIncomeErr: &lknpd.APIError{Operation: "POST /cancel", Status: http.StatusBadRequest, Body: "raw-secret"},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:retryable")

	response := postCancel(t, server, "shm:retryable", nil)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", response.StatusCode, body)
	}
	if stringsContains(body, "raw-secret") {
		t.Fatalf("leaked body: %s", body)
	}
	stored, ok := store.GetReceipt("shm:retryable")
	if !ok || stored.Status != "created" || stored.LastError != errCodeValidationRejected {
		t.Fatalf("stored = %+v ok=%v", stored, ok)
	}

	fake.cancelIncomeErr = nil
	again := postCancel(t, server, "shm:retryable", nil)
	again.Body.Close()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("retry expected 200, got %d", again.StatusCode)
	}
	if fake.cancelIncomeCalls.Load() != 2 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
}

func TestCancelAmbiguousNetworkBecomesCancelUnknown(t *testing.T) {
	fake := &fakeLKNPD{
		cancelIncomeErr: &lknpd.APIError{Operation: "POST /cancel", Status: 0, Err: context.DeadlineExceeded, Body: "raw-timeout"},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:unknown")

	response := postCancel(t, server, "shm:unknown", nil)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !stringsContains(body, errCodeCancelUnknown) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if stringsContains(body, "raw-timeout") || stringsContains(body, "receipt-test-1") {
		t.Fatalf("leaked: %s", body)
	}
	stored, ok := store.GetReceipt("shm:unknown")
	if !ok || stored.Status != receiptStatusCancelUnknown || stored.LastError != errCodeUpstreamTimeout {
		t.Fatalf("stored = %+v ok=%v", stored, ok)
	}

	retry := postCancel(t, server, "shm:unknown", nil)
	retry.Body.Close()
	if retry.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d", retry.StatusCode)
	}
	if fake.cancelIncomeCalls.Load() != 1 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
}

func TestCancelPersistedCancellingDoesNotRetryUpstream(t *testing.T) {
	fake := &fakeLKNPD{}
	server, store := newTestServer(t, fake)
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:stuck")
	record, ok := store.GetReceipt("shm:stuck")
	if !ok {
		t.Fatal("missing receipt")
	}
	record.Status = receiptStatusCancelling
	if err := store.PutReceipt(record); err != nil {
		t.Fatal(err)
	}

	response := postCancel(t, server, "shm:stuck", nil)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !stringsContains(body, errCodeCancelReconcile) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if fake.cancelIncomeCalls.Load() != 0 {
		t.Fatalf("CancelIncome calls = %d", fake.cancelIncomeCalls.Load())
	}
}

func TestCancelStatePersistFailureIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLKNPD{}
	failing := &failingTransitionStore{Store: store, failOnCall: 1}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(New("test-api-key", "Test service", fake, failing, logger).Handler())
	defer server.Close()
	seedCreatedReceipt(t, server, "shm:persist")

	response := postCancel(t, server, "shm:persist", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.StatusCode)
	}
	if fake.cancelIncomeCalls.Load() != 0 {
		t.Fatalf("upstream must not be called, got %d", fake.cancelIncomeCalls.Load())
	}
	stored, ok := store.GetReceipt("shm:persist")
	if !ok || stored.Status != "created" {
		t.Fatalf("stored = %+v ok=%v", stored, ok)
	}
}

type failingTransitionStore struct {
	*state.Store
	failOnCall int
	calls      int
}

func (f *failingTransitionStore) TransitionReceipt(externalID, expectedStatus string, updated state.ReceiptRecord) error {
	f.calls++
	if f.calls == f.failOnCall {
		return errors.New("state persist failed")
	}
	return f.Store.TransitionReceipt(externalID, expectedStatus, updated)
}

func stringsContains(body []byte, needle string) bool {
	return bytes.Contains(body, []byte(needle))
}
