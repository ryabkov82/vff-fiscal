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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func newServerWithStore(t *testing.T, client lknpdClient, store receiptStore, logger *slog.Logger) *httptest.Server {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	api := New("test-api-key", "Test service", client, store, logger)
	return httptest.NewServer(api.Handler())
}

func TestCreateReceiptDefiniteErrorStoresFailedEvent(t *testing.T) {
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "invalid amount"},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "fail-evt:1", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", response.StatusCode)
	}

	record, ok := store.GetReceipt("fail-evt:1")
	if !ok || record.Status != "failed" {
		t.Fatalf("unexpected record: %+v, ok=%v", record, ok)
	}
	if record.LastError != errCodeValidationRejected {
		t.Fatalf("LastError must be safe code, got %q", record.LastError)
	}

	event, ok := store.GetNotificationEvent(notificationEventID("fail-evt:1", state.EventTypeReceiptFailed))
	if !ok {
		t.Fatal("expected receipt.failed event")
	}
	if event.EventType != state.EventTypeReceiptFailed {
		t.Fatalf("unexpected event type: %q", event.EventType)
	}
	if event.ErrorCode != errCodeValidationRejected {
		t.Fatalf("unexpected event error code: %q", event.ErrorCode)
	}
	if event.ReceiptExternalID != "fail-evt:1" || event.Amount != "10.00" {
		t.Fatalf("unexpected event payload: %+v", event)
	}
	if event.DeliveryStatus != state.NotificationDeliveryPending {
		t.Fatalf("unexpected delivery status: %q", event.DeliveryStatus)
	}
}

func TestCreateReceiptAmbiguousStoresUnknownEvent(t *testing.T) {
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: context.DeadlineExceeded},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "unknown-evt:1", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.StatusCode)
	}

	record, ok := store.GetReceipt("unknown-evt:1")
	if !ok || record.Status != "unknown" {
		t.Fatalf("unexpected record: %+v, ok=%v", record, ok)
	}
	if record.LastError != errCodeUpstreamTimeout {
		t.Fatalf("LastError must be safe code, got %q", record.LastError)
	}

	event, ok := store.GetNotificationEvent(notificationEventID("unknown-evt:1", state.EventTypeReceiptUnknown))
	if !ok || event.EventType != state.EventTypeReceiptUnknown {
		t.Fatalf("expected receipt.unknown event, got %+v ok=%v", event, ok)
	}
	if event.ErrorCode != errCodeUpstreamTimeout {
		t.Fatalf("unexpected event error code: %q", event.ErrorCode)
	}
}

func TestCreateReceiptErrorCodesAndStatuses(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantHTTP   int
		wantStatus string
		wantEvent  string
		wantCode   string
	}{
		{
			name:       "timeout",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: context.DeadlineExceeded},
			wantHTTP:   http.StatusServiceUnavailable,
			wantStatus: "unknown",
			wantEvent:  state.EventTypeReceiptUnknown,
			wantCode:   errCodeUpstreamTimeout,
		},
		{
			name:       "network transport",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: notTimeout{}},
			wantHTTP:   http.StatusServiceUnavailable,
			wantStatus: "unknown",
			wantEvent:  state.EventTypeReceiptUnknown,
			wantCode:   errCodeUpstreamTransport,
		},
		{
			name:       "http 5xx",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadGateway, Body: "gw", Ambiguous: true},
			wantHTTP:   http.StatusServiceUnavailable,
			wantStatus: "unknown",
			wantEvent:  state.EventTypeReceiptUnknown,
			wantCode:   errCodeUpstream5xx,
		},
		{
			name:       "http 4xx",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "bad"},
			wantHTTP:   http.StatusUnprocessableEntity,
			wantStatus: "failed",
			wantEvent:  state.EventTypeReceiptFailed,
			wantCode:   errCodeValidationRejected,
		},
		{
			name:       "http 429",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusTooManyRequests},
			wantHTTP:   http.StatusUnprocessableEntity,
			wantStatus: "failed",
			wantEvent:  state.EventTypeReceiptFailed,
			wantCode:   errCodeRateLimited,
		},
		{
			name:       "empty approved uuid",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: lknpd.ErrEmptyApprovedReceiptUUID},
			wantHTTP:   http.StatusServiceUnavailable,
			wantStatus: "unknown",
			wantEvent:  state.EventTypeReceiptUnknown,
			wantCode:   errCodeInvalidResponse,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLKNPD{createIncomeErr: tc.err}
			server, store := newTestServer(t, fake)
			defer server.Close()

			externalID := "codes:" + tc.name
			response := postReceiptPayload(t, server, map[string]string{
				"external_id": externalID,
				"amount":      "10.00",
			})
			response.Body.Close()
			if response.StatusCode != tc.wantHTTP {
				t.Fatalf("expected HTTP %d, got %d", tc.wantHTTP, response.StatusCode)
			}

			record, ok := store.GetReceipt(externalID)
			if !ok || record.Status != tc.wantStatus {
				t.Fatalf("unexpected record: %+v ok=%v", record, ok)
			}
			if record.LastError != tc.wantCode {
				t.Fatalf("unexpected LastError %q, want %q", record.LastError, tc.wantCode)
			}
			event, ok := store.GetNotificationEvent(notificationEventID(externalID, tc.wantEvent))
			if !ok || event.ErrorCode != tc.wantCode {
				t.Fatalf("unexpected event %+v ok=%v", event, ok)
			}
			if fake.createIncomeCalls.Load() != 1 {
				t.Fatalf("expected one CreateIncome call, got %d", fake.createIncomeCalls.Load())
			}
		})
	}
}

func TestCreateReceiptSuccessStoresNoEvent(t *testing.T) {
	fake := &fakeLKNPD{}
	server, store := newTestServer(t, fake)
	defer server.Close()

	response := postReceipt(t, server, "success-noevt:1", true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", response.StatusCode)
	}

	if _, ok := store.GetNotificationEvent(notificationEventID("success-noevt:1", state.EventTypeReceiptFailed)); ok {
		t.Fatal("no failed event expected for successful creation")
	}
	if _, ok := store.GetNotificationEvent(notificationEventID("success-noevt:1", state.EventTypeReceiptUnknown)); ok {
		t.Fatal("no unknown event expected for successful creation")
	}
}

func TestCreateReceiptRepeatDoesNotDuplicateEventOrCall(t *testing.T) {
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "bad"},
	}
	server, store := newTestServer(t, fake)
	defer server.Close()

	first := postReceipt(t, server, "repeat-fail:1", true)
	first.Body.Close()
	if first.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", first.StatusCode)
	}

	second := postReceipt(t, server, "repeat-fail:1", true)
	second.Body.Close()

	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected exactly one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}

	record, ok := store.GetReceipt("repeat-fail:1")
	if !ok || record.Status != "failed" {
		t.Fatalf("unexpected record: %+v ok=%v", record, ok)
	}
	if _, ok := store.GetNotificationEvent(notificationEventID("repeat-fail:1", state.EventTypeReceiptFailed)); !ok {
		t.Fatal("expected exactly one failed event to remain")
	}
}

func TestCreateReceiptPersistenceErrorBeforeRename(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "state-dir")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(parent, "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "bad"},
		onCreateIncome: func() {
			// Replace the state directory with a regular file so the failure
			// transition fails in MkdirAll, before any temp write or rename.
			_ = os.RemoveAll(parent)
			_ = os.WriteFile(parent, []byte("x"), 0o600)
		},
	}
	server := newServerWithStore(t, fake, store, nil)
	defer server.Close()

	response := postReceipt(t, server, "prerename:1", true)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.StatusCode)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected exactly one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}

	record, ok := store.GetReceipt("prerename:1")
	if !ok || record.Status != "creating" {
		t.Fatalf("receipt must remain creating in memory: %+v ok=%v", record, ok)
	}
	if _, ok := store.GetNotificationEvent(notificationEventID("prerename:1", state.EventTypeReceiptFailed)); ok {
		t.Fatal("event must not be present after pre-rename persistence failure")
	}
}

type durabilityUncertainStore struct {
	*state.Store
}

func (d durabilityUncertainStore) TransitionReceiptWithEvent(externalID, expected string, updated state.ReceiptRecord, event state.NotificationEvent) error {
	if err := d.Store.TransitionReceiptWithEvent(externalID, expected, updated, event); err != nil {
		return err
	}
	return state.ErrStateDurabilityUncertain
}

func TestCreateReceiptDurabilityUncertainIsTreatedAsCommitted(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "bad"},
	}
	server := newServerWithStore(t, fake, durabilityUncertainStore{store}, nil)
	defer server.Close()

	response := postReceipt(t, server, "durable:1", true)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected upstream error 422 (transition treated as committed), got %d", response.StatusCode)
	}

	record, ok := store.GetReceipt("durable:1")
	if !ok || record.Status != "failed" {
		t.Fatalf("receipt must be committed as failed: %+v ok=%v", record, ok)
	}
	if _, ok := store.GetNotificationEvent(notificationEventID("durable:1", state.EventTypeReceiptFailed)); !ok {
		t.Fatal("event must be committed despite durability uncertainty")
	}
}

func TestCreateReceiptFailureLeaksNoSecretMarkers(t *testing.T) {
	const secretBody = "FNS_RAW_BODY_SECRET inn=123456789012 token=SECRET_TOKEN"
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: secretBody},
	}
	server := newServerWithStore(t, fake, store, logger)
	defer server.Close()

	response := postReceipt(t, server, "leak:1", true)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()

	markers := []string{"FNS_RAW_BODY_SECRET", "123456789012", "SECRET_TOKEN"}

	for _, marker := range markers {
		if strings.Contains(string(body), marker) {
			t.Fatalf("HTTP response leaked marker %q", marker)
		}
		if strings.Contains(logs.String(), marker) {
			t.Fatalf("logs leaked marker %q", marker)
		}
	}

	record, _ := store.GetReceipt("leak:1")
	event, _ := store.GetNotificationEvent(notificationEventID("leak:1", state.EventTypeReceiptFailed))
	stateDump := record.LastError + "|" + event.ErrorCode + "|" + event.EventType + "|" + event.ReceiptExternalID + "|" + event.Amount
	for _, marker := range markers {
		if strings.Contains(stateDump, marker) {
			t.Fatalf("state leaked marker %q", marker)
		}
	}
	if record.LastError != errCodeValidationRejected {
		t.Fatalf("unexpected LastError %q", record.LastError)
	}
}

// TestCreateReceiptTransportErrorLeaksNoMarkers drives a transport-level upstream
// failure whose wrapped error text is stuffed with secret markers (host, URL,
// token, INN) and proves none of them reach the HTTP response, the logger output
// or the persisted safe code.
func TestCreateReceiptTransportErrorLeaksNoMarkers(t *testing.T) {
	markers := []string{"SECRET_HOST", "TRANSPORT_SECRET", "SECRET_TOKEN", "123456789012", "https://lknpd.nalog.ru/api"}
	transportErr := errors.New("dial tcp SECRET_HOST:443: refused TRANSPORT_SECRET token=SECRET_TOKEN inn=123456789012 https://lknpd.nalog.ru/api")

	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: transportErr},
	}
	server := newServerWithStore(t, fake, store, logger)
	defer server.Close()

	response := postReceipt(t, server, "transport-leak:1", true)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.StatusCode)
	}

	for _, marker := range markers {
		if strings.Contains(string(body), marker) {
			t.Fatalf("HTTP response leaked transport marker %q", marker)
		}
		if strings.Contains(logs.String(), marker) {
			t.Fatalf("logs leaked transport marker %q", marker)
		}
	}

	record, ok := store.GetReceipt("transport-leak:1")
	if !ok || record.Status != "unknown" {
		t.Fatalf("unexpected record: %+v ok=%v", record, ok)
	}
	if record.LastError != errCodeUpstreamTransport {
		t.Fatalf("expected safe transport code, got %q", record.LastError)
	}
}

// TestCreateReceiptSuccessPersistFailureLeaksNoReceiptUUID makes the FNS call
// succeed with a receipt whose UUID and URLs carry secret markers, then forces
// the local state write to fail. The success-path error log and HTTP response
// must expose neither the receipt UUID/URLs nor any other receipt content.
func TestCreateReceiptSuccessPersistFailureLeaksNoReceiptUUID(t *testing.T) {
	markers := []string{"RECEIPT_UUID_SECRET", "PRINT_URL_SECRET", "JSON_URL_SECRET"}

	dir := t.TempDir()
	parent := filepath.Join(dir, "state-dir")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(parent, "state.json"), state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &fakeLKNPD{
		receipt: lknpd.Receipt{
			ReceiptUUID: "RECEIPT_UUID_SECRET",
			PrintURL:    "https://host/RECEIPT_UUID_SECRET/PRINT_URL_SECRET",
			JSONURL:     "https://host/RECEIPT_UUID_SECRET/JSON_URL_SECRET",
		},
		onCreateIncome: func() {
			// Break the state directory so the post-success PutReceipt fails.
			_ = os.RemoveAll(parent)
			_ = os.WriteFile(parent, []byte("x"), 0o600)
		},
	}
	server := newServerWithStore(t, fake, store, logger)
	defer server.Close()

	response := postReceipt(t, server, "success-persist-fail:1", true)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", response.StatusCode)
	}
	if fake.createIncomeCalls.Load() != 1 {
		t.Fatalf("expected exactly one CreateIncome call, got %d", fake.createIncomeCalls.Load())
	}

	for _, marker := range markers {
		if strings.Contains(string(body), marker) {
			t.Fatalf("HTTP response leaked receipt marker %q", marker)
		}
		if strings.Contains(logs.String(), marker) {
			t.Fatalf("logs leaked receipt marker %q", marker)
		}
	}
}

// TestCreateReceiptFailurePersistsSafeStateOnDisk exercises the full failure
// path against a real *state.Store, then reopens the on-disk state.json to prove
// that (a) no secret marker survives anywhere externally observable and (b) the
// receipt and its notification event are durably persisted with the expected
// pending-delivery fields.
func TestCreateReceiptFailurePersistsSafeStateOnDisk(t *testing.T) {
	const secretBody = "FNS_RAW_BODY_SECRET inn=123456789012 token=SECRET_TOKEN"
	markers := []string{"FNS_RAW_BODY_SECRET", "123456789012", "SECRET_TOKEN"}

	statePath := filepath.Join(t.TempDir(), "state.json")
	store, err := state.Open(statePath, state.AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	fake := &fakeLKNPD{
		createIncomeErr: &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: secretBody},
	}
	server := newServerWithStore(t, fake, store, logger)
	defer server.Close()

	const externalID = "leak-disk:1"
	response := postReceipt(t, server, externalID, true)
	httpBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", response.StatusCode)
	}

	fileBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading persisted state: %v", err)
	}

	for _, marker := range markers {
		if strings.Contains(string(fileBytes), marker) {
			t.Fatalf("persisted state.json leaked marker %q", marker)
		}
		if strings.Contains(string(httpBody), marker) {
			t.Fatalf("HTTP response leaked marker %q", marker)
		}
		if strings.Contains(logs.String(), marker) {
			t.Fatalf("logs leaked marker %q", marker)
		}
	}

	// Reopen the store from disk to confirm the transition is truly durable and
	// survives a fresh load rather than only living in memory.
	reopened, err := state.Open(statePath, state.AuthState{})
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}

	record, ok := reopened.GetReceipt(externalID)
	if !ok {
		t.Fatal("receipt missing after reopening state")
	}
	if record.Status != "failed" {
		t.Fatalf("unexpected persisted status %q", record.Status)
	}
	if record.LastError != errCodeValidationRejected {
		t.Fatalf("persisted LastError must be safe code, got %q", record.LastError)
	}

	eventID := notificationEventID(externalID, state.EventTypeReceiptFailed)
	event, ok := reopened.GetNotificationEvent(eventID)
	if !ok {
		t.Fatal("notification event missing after reopening state")
	}

	if event.SchemaVersion != notificationSchemaVersion {
		t.Fatalf("unexpected schema version %d", event.SchemaVersion)
	}
	if event.EventType != state.EventTypeReceiptFailed {
		t.Fatalf("unexpected event type %q", event.EventType)
	}
	if event.ErrorCode != errCodeValidationRejected {
		t.Fatalf("unexpected event error code %q", event.ErrorCode)
	}
	if event.DeliveryStatus != state.NotificationDeliveryPending {
		t.Fatalf("unexpected delivery status %q", event.DeliveryStatus)
	}
	if event.Attempts != 0 {
		t.Fatalf("expected 0 attempts, got %d", event.Attempts)
	}
	if event.OccurredAt.IsZero() || !event.OccurredAt.Equal(event.NextAttemptAt) {
		t.Fatalf("expected OccurredAt == NextAttemptAt and non-zero, got %v / %v", event.OccurredAt, event.NextAttemptAt)
	}
	if event.LastAttemptAt != nil {
		t.Fatalf("expected nil LastAttemptAt, got %v", event.LastAttemptAt)
	}
	if event.DeliveredAt != nil {
		t.Fatalf("expected nil DeliveredAt, got %v", event.DeliveredAt)
	}
	if event.LastResultCode != "" {
		t.Fatalf("expected empty LastResultCode, got %q", event.LastResultCode)
	}

	// The persisted event must also stay free of secret markers.
	dump := record.LastError + "|" + event.ErrorCode + "|" + event.EventType + "|" + event.ReceiptExternalID + "|" + event.Amount + "|" + event.EventID
	for _, marker := range markers {
		if strings.Contains(dump, marker) {
			t.Fatalf("persisted event leaked marker %q", marker)
		}
	}
}

func authGet(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-api-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func authPost(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("{}"))
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

// TestUserEndpointUpstreamErrorScope pins the externally observable error
// responses of GET /v1/user and confirms no raw upstream body ever escapes.
func TestUserEndpointUpstreamErrorScope(t *testing.T) {
	const secretBody = "FNS_RAW_BODY_SECRET inn=123456789012"
	tests := []struct {
		name     string
		err      error
		wantHTTP int
		wantCode string
	}{
		{
			name:     "4xx rejected",
			err:      &lknpd.APIError{Operation: "GET /user", Status: http.StatusUnauthorized, Body: secretBody},
			wantHTTP: http.StatusUnprocessableEntity,
			wantCode: errCodeAuthRejected,
		},
		{
			name:     "5xx unknown",
			err:      &lknpd.APIError{Operation: "GET /user", Status: http.StatusBadGateway, Body: secretBody},
			wantHTTP: http.StatusServiceUnavailable,
			wantCode: errCodeUpstream5xx,
		},
		{
			name:     "generic error",
			err:      context.DeadlineExceeded,
			wantHTTP: http.StatusBadGateway,
			wantCode: errCodeUpstreamError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeLKNPD{getUserErr: tc.err}
			server, _ := newTestServer(t, fake)
			defer server.Close()

			response := authGet(t, server, "/v1/user")
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()

			if response.StatusCode != tc.wantHTTP {
				t.Fatalf("expected HTTP %d, got %d", tc.wantHTTP, response.StatusCode)
			}
			if got := errorField(t, body); got != tc.wantCode {
				t.Fatalf("expected error code %q, got %q", tc.wantCode, got)
			}
			if strings.Contains(string(body), "FNS_RAW_BODY_SECRET") || strings.Contains(string(body), "123456789012") {
				t.Fatalf("user endpoint leaked upstream body: %s", body)
			}
		})
	}
}

// TestCancelEndpointUpstreamErrorScope pins the error responses of the cancel
// endpoint, confirms no raw upstream body escapes, and verifies the cancellation
// state flow is unchanged (a failed cancel leaves the receipt "created").
func TestCancelEndpointUpstreamErrorScope(t *testing.T) {
	const secretBody = "FNS_RAW_BODY_SECRET inn=123456789012"
	fake := &fakeLKNPD{}
	server, store := newTestServer(t, fake)
	defer server.Close()

	created := postReceipt(t, server, "cancel-scope:1", true)
	created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", created.StatusCode)
	}

	fake.cancelIncomeErr = &lknpd.APIError{Operation: "POST /cancel", Status: http.StatusBadGateway, Body: secretBody, Ambiguous: true}

	response := authPost(t, server, "/v1/receipts/cancel-scope:1/cancel")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for ambiguous cancel error, got %d", response.StatusCode)
	}
	if got := errorField(t, body); got != errCodeUpstream5xx {
		t.Fatalf("expected error code %q, got %q", errCodeUpstream5xx, got)
	}
	if strings.Contains(string(body), "FNS_RAW_BODY_SECRET") || strings.Contains(string(body), "123456789012") {
		t.Fatalf("cancel endpoint leaked upstream body: %s", body)
	}

	// Cancellation state flow is unchanged: the receipt remains created.
	record, ok := store.GetReceipt("cancel-scope:1")
	if !ok || record.Status != "created" {
		t.Fatalf("failed cancel must leave receipt created, got %+v ok=%v", record, ok)
	}
}

func errorField(t *testing.T, body []byte) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decoding error body %q: %v", body, err)
	}
	message, _ := parsed["error"].(string)
	return message
}
