package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

type fakeLKNPD struct {
	createIncomeCalls atomic.Int32
	createIncomeGate  chan struct{}
	createIncomeErr   error
	receipt           lknpd.Receipt
}

func (f *fakeLKNPD) GetUser(context.Context) (lknpd.UserInfo, error) {
	return lknpd.UserInfo{"status": "ok"}, nil
}

func (f *fakeLKNPD) CreateIncome(_ context.Context, _ lknpd.CreateIncomeParams) (lknpd.Receipt, error) {
	f.createIncomeCalls.Add(1)
	if f.createIncomeGate != nil {
		<-f.createIncomeGate
	}
	if f.createIncomeErr != nil {
		return lknpd.Receipt{}, f.createIncomeErr
	}
	if f.receipt.ReceiptUUID == "" {
		return lknpd.Receipt{
			ReceiptUUID: "receipt-test-1",
			PrintURL:    "https://example.test/print",
			JSONURL:     "https://example.test/json",
		}, nil
	}
	return f.receipt, nil
}

func (f *fakeLKNPD) CancelIncome(context.Context, string, string, time.Time) error {
	return nil
}

func newTestServer(t *testing.T, client lknpdClient) (*httptest.Server, *state.Store) {
	t.Helper()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New("test-api-key", "Test service", client, store, logger)
	return httptest.NewServer(api.Handler()), store
}

func postReceiptRequest(externalID string, auth bool) (*http.Request, error) {
	return postReceiptPayloadRequest(map[string]string{
		"external_id": externalID,
		"amount":      "10.00",
	}, auth)
}

func postReceiptPayloadRequest(payload map[string]string, auth bool) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequest(http.MethodPost, "/v1/receipts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if auth {
		request.Header.Set("Authorization", "Bearer test-api-key")
	}
	return request, nil
}

func postReceiptPayload(t *testing.T, server *httptest.Server, payload map[string]string) *http.Response {
	t.Helper()

	request, err := postReceiptPayloadRequest(payload, true)
	if err != nil {
		t.Fatal(err)
	}
	request.URL.Scheme = "http"
	request.URL.Host = server.Listener.Addr().String()

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeErrorMessage(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	message, _ := body["error"].(string)
	return message
}

func postReceipt(t *testing.T, server *httptest.Server, externalID string, auth bool) *http.Response {
	t.Helper()

	request, err := postReceiptRequest(externalID, auth)
	if err != nil {
		t.Fatal(err)
	}
	request.URL.Scheme = "http"
	request.URL.Host = server.Listener.Addr().String()

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeReceipt(t *testing.T, response *http.Response) state.ReceiptRecord {
	t.Helper()
	defer response.Body.Close()

	var record state.ReceiptRecord
	if err := json.NewDecoder(response.Body).Decode(&record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestCreateReceiptUnauthorized(t *testing.T) {
	server, _ := newTestServer(t, &fakeLKNPD{})
	defer server.Close()

	response := postReceipt(t, server, "unauth:1", false)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}
}

func TestCreateReceiptFirstRequestCreates(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "first:1", true)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", response.StatusCode)
	}
	record := decodeReceipt(t, response)
	if record.Status != "created" || record.ReceiptUUID != "receipt-test-1" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptRepeatAfterCreatedIsIdempotent(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceipt(t, server, "repeat:1", true)
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	firstRecord := decodeReceipt(t, first)

	second := postReceipt(t, server, "repeat:1", true)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", second.StatusCode)
	}
	secondRecord := decodeReceipt(t, second)
	if secondRecord.ReceiptUUID != firstRecord.ReceiptUUID {
		t.Fatalf("expected same receipt uuid, got %+v and %+v", firstRecord, secondRecord)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptConcurrentRequestsCallCreateIncomeOnce(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeLKNPD{createIncomeGate: gate}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			request, err := postReceiptRequest("concurrent:1", true)
			if err != nil {
				t.Error(err)
				return
			}
			request.URL.Scheme = "http"
			request.URL.Host = server.Listener.Addr().String()
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
		}()
	}

	close(start)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.createIncomeCalls.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call while blocked, got %d", fake.createIncomeCalls.Load())
	}

	close(gate)
	wg.Wait()

	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected exactly one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptAmbiguousErrorStoresUnknown(t *testing.T) {
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{
			Operation: "POST /income",
			Ambiguous: true,
			Err:       context.DeadlineExceeded,
		},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "ambiguous:1", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.StatusCode)
	}

	stored, ok := store.GetReceipt("ambiguous:1")
	if !ok || stored.Status != "unknown" {
		t.Fatalf("stored record: %+v, exists=%v", stored, ok)
	}
}

func TestCreateReceiptDefiniteErrorStoresFailed(t *testing.T) {
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{
			Operation: "POST /income",
			Status:    400,
			Body:      "invalid amount",
		},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "failed:1", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", response.StatusCode)
	}

	stored, ok := store.GetReceipt("failed:1")
	if !ok || stored.Status != "failed" {
		t.Fatalf("stored record: %+v, exists=%v", stored, ok)
	}
}

func TestGetReceiptNotFound(t *testing.T) {
	server, _ := newTestServer(t, &fakeLKNPD{})
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/receipts/missing:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-api-key")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
}

func TestCreateReceiptDuplicateDifferentAmount(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-amount:1",
		"amount":      "10.00",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-amount:1",
		"amount":      "11.00",
	})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", second.StatusCode)
	}
	if got := decodeErrorMessage(t, second); got != "external_id already exists with different receipt data" {
		t.Fatalf("unexpected error: %q", got)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptDuplicateDifferentServiceName(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceiptPayload(t, server, map[string]string{
		"external_id":  "dup-service:1",
		"amount":       "10.00",
		"service_name": "Test service",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := postReceiptPayload(t, server, map[string]string{
		"external_id":  "dup-service:1",
		"amount":       "10.00",
		"service_name": "Other service",
	})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", second.StatusCode)
	}
	if got := decodeErrorMessage(t, second); got != "external_id already exists with different receipt data" {
		t.Fatalf("unexpected error: %q", got)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptDuplicateDifferentOperationTime(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceiptPayload(t, server, map[string]string{
		"external_id":    "dup-time:1",
		"amount":         "10.00",
		"operation_time": "2026-07-09T16:30:00+03:00",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := postReceiptPayload(t, server, map[string]string{
		"external_id":    "dup-time:1",
		"amount":         "10.00",
		"operation_time": "2026-07-09T17:30:00+03:00",
	})
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", second.StatusCode)
	}
	if got := decodeErrorMessage(t, second); got != "external_id already exists with different receipt data" {
		t.Fatalf("unexpected error: %q", got)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptDuplicateOmittedServiceNameMatchesDefault(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-default-service:1",
		"amount":      "10.00",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-default-service:1",
		"amount":      "10.00",
	})
	if second.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", second.StatusCode)
	}
	second.Body.Close()
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}

func TestCreateReceiptDuplicateEquivalentAmountFormats(t *testing.T) {
	fake := &fakeLKNPD{}
	server, _ := newTestServer(t, fake)
	defer server.Close()

	first := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-amount-format:1",
		"amount":      "150.00",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := postReceiptPayload(t, server, map[string]string{
		"external_id": "dup-amount-format:1",
		"amount":      "150",
	})
	if second.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", second.StatusCode)
	}
	second.Body.Close()
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}
}
