package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

var eventIDPattern = regexp.MustCompile(`^evt_[0-9a-f]{64}$`)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestNotificationEventIDVersionedFormat(t *testing.T) {
	externalID := "shm:secret-external-id"

	failed := notificationEventID(externalID, state.EventTypeReceiptFailed)
	if !eventIDPattern.MatchString(failed) {
		t.Fatalf("event id must be evt_ + 64 lowercase hex, got %q", failed)
	}

	// Deterministic: identical inputs always map to the same id.
	if again := notificationEventID(externalID, state.EventTypeReceiptFailed); again != failed {
		t.Fatalf("event id is not deterministic: %q != %q", failed, again)
	}

	// Different event types must produce different ids for the same receipt.
	unknown := notificationEventID(externalID, state.EventTypeReceiptUnknown)
	if failed == unknown {
		t.Fatal("failed and unknown event ids must differ")
	}
	if !eventIDPattern.MatchString(unknown) {
		t.Fatalf("unknown event id malformed: %q", unknown)
	}

	// The plaintext external id must never be embedded in the id.
	if strings.Contains(failed, externalID) || strings.Contains(unknown, externalID) {
		t.Fatalf("event id leaked plaintext external id: %q / %q", failed, unknown)
	}
}

func TestNotificationEventIDIsVersioned(t *testing.T) {
	externalID := "shm:versioned"
	got := notificationEventID(externalID, state.EventTypeReceiptFailed)

	// Recomputing with the pinned canonical layout must match; a change in the
	// canonical string (version, separators, field order) would break this.
	unversioned := sha256Hex("receipt.failed\x00" + externalID)
	if strings.TrimPrefix(got, "evt_") == unversioned {
		t.Fatal("event id must derive from the versioned canonical string, not the bare payload")
	}
	if strings.TrimPrefix(got, "evt_") != sha256Hex("v1\x00receipt.failed\x00"+externalID) {
		t.Fatalf("event id does not match the versioned canonical string: %q", got)
	}
}

type notTimeout struct{}

func (notTimeout) Error() string { return "connection refused" }

func TestUpstreamErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "timeout after send",
			err:  &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: context.DeadlineExceeded},
			want: errCodeUpstreamTimeout,
		},
		{
			name: "transport error after send",
			err:  &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: notTimeout{}},
			want: errCodeUpstreamTransport,
		},
		{
			name: "upstream 5xx",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadGateway, Body: "gateway", Ambiguous: true},
			want: errCodeUpstream5xx,
		},
		{
			name: "invalid successful response",
			err:  &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: lknpd.ErrEmptyApprovedReceiptUUID},
			want: errCodeInvalidResponse,
		},
		{
			name: "authentication rejected 401",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusUnauthorized},
			want: errCodeAuthRejected,
		},
		{
			name: "authentication rejected 403",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusForbidden},
			want: errCodeAuthRejected,
		},
		{
			name: "rate limited 429",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusTooManyRequests},
			want: errCodeRateLimited,
		},
		{
			name: "validation rejected 400",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "invalid amount"},
			want: errCodeValidationRejected,
		},
		{
			name: "validation rejected 422",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusUnprocessableEntity},
			want: errCodeValidationRejected,
		},
		{
			name: "other request rejected 404",
			err:  &lknpd.APIError{Operation: "POST /income", Status: http.StatusNotFound},
			want: errCodeRequestRejected,
		},
		{
			name: "generic non-api error",
			err:  errors.New("something internal"),
			want: errCodeUpstreamError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamErrorCode(tc.err); got != tc.want {
				t.Fatalf("upstreamErrorCode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyUpstreamOutcomeIsConsistent(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus string
		wantEvent  string
		wantHTTP   int
	}{
		{
			name:       "timeout is unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: context.DeadlineExceeded},
			wantCode:   errCodeUpstreamTimeout,
			wantStatus: receiptStatusUnknown,
			wantEvent:  state.EventTypeReceiptUnknown,
			wantHTTP:   http.StatusServiceUnavailable,
		},
		{
			name:       "transport is unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: notTimeout{}},
			wantCode:   errCodeUpstreamTransport,
			wantStatus: receiptStatusUnknown,
			wantEvent:  state.EventTypeReceiptUnknown,
			wantHTTP:   http.StatusServiceUnavailable,
		},
		{
			name:       "5xx is unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadGateway, Body: "gw", Ambiguous: true},
			wantCode:   errCodeUpstream5xx,
			wantStatus: receiptStatusUnknown,
			wantEvent:  state.EventTypeReceiptUnknown,
			wantHTTP:   http.StatusServiceUnavailable,
		},
		{
			name:       "empty uuid is unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Ambiguous: true, Err: lknpd.ErrEmptyApprovedReceiptUUID},
			wantCode:   errCodeInvalidResponse,
			wantStatus: receiptStatusUnknown,
			wantEvent:  state.EventTypeReceiptUnknown,
			wantHTTP:   http.StatusServiceUnavailable,
		},
		{
			name:       "4xx validation is failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Body: "bad"},
			wantCode:   errCodeValidationRejected,
			wantStatus: receiptStatusFailed,
			wantEvent:  state.EventTypeReceiptFailed,
			wantHTTP:   http.StatusUnprocessableEntity,
		},
		{
			name:       "429 is failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusTooManyRequests},
			wantCode:   errCodeRateLimited,
			wantStatus: receiptStatusFailed,
			wantEvent:  state.EventTypeReceiptFailed,
			wantHTTP:   http.StatusUnprocessableEntity,
		},
		{
			name:       "generic is failed gateway",
			err:        errors.New("boom"),
			wantCode:   errCodeUpstreamError,
			wantStatus: receiptStatusFailed,
			wantEvent:  state.EventTypeReceiptFailed,
			wantHTTP:   http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstream(tc.err)
			if got.Code != tc.wantCode || got.ReceiptStatus != tc.wantStatus || got.EventType != tc.wantEvent || got.HTTPStatus != tc.wantHTTP {
				t.Fatalf("classifyUpstream = %+v, want code=%q status=%q event=%q http=%d",
					got, tc.wantCode, tc.wantStatus, tc.wantEvent, tc.wantHTTP)
			}
			assertOutcomeSelfConsistent(t, got)
		})
	}
}

// TestClassifyUpstreamDefensiveContradictions covers deliberately contradictory
// APIError.Status / APIError.Ambiguous combinations to prove the outcome is
// decided by the error category alone and can never yield an inconsistent pair
// such as "unknown"+auth_rejected or "failed"+upstream_5xx.
func TestClassifyUpstreamDefensiveContradictions(t *testing.T) {
	tests := []struct {
		name       string
		err        *lknpd.APIError
		wantCode   string
		wantStatus string
	}{
		{
			name:       "401 flagged ambiguous stays failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusUnauthorized, Ambiguous: true},
			wantCode:   errCodeAuthRejected,
			wantStatus: receiptStatusFailed,
		},
		{
			name:       "403 flagged ambiguous stays failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusForbidden, Ambiguous: true},
			wantCode:   errCodeAuthRejected,
			wantStatus: receiptStatusFailed,
		},
		{
			name:       "400 flagged ambiguous stays failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusBadRequest, Ambiguous: true},
			wantCode:   errCodeValidationRejected,
			wantStatus: receiptStatusFailed,
		},
		{
			name:       "429 flagged ambiguous stays failed",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusTooManyRequests, Ambiguous: true},
			wantCode:   errCodeRateLimited,
			wantStatus: receiptStatusFailed,
		},
		{
			name:       "5xx not flagged ambiguous stays unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusInternalServerError, Ambiguous: false},
			wantCode:   errCodeUpstream5xx,
			wantStatus: receiptStatusUnknown,
		},
		{
			name:       "503 not flagged ambiguous stays unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Status: http.StatusServiceUnavailable, Ambiguous: false},
			wantCode:   errCodeUpstream5xx,
			wantStatus: receiptStatusUnknown,
		},
		{
			name:       "transport not flagged ambiguous stays unknown",
			err:        &lknpd.APIError{Operation: "POST /income", Status: 0, Ambiguous: false, Err: notTimeout{}},
			wantCode:   errCodeUpstreamTransport,
			wantStatus: receiptStatusUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstream(tc.err)
			if got.Code != tc.wantCode || got.ReceiptStatus != tc.wantStatus {
				t.Fatalf("classifyUpstream = %+v, want code=%q status=%q", got, tc.wantCode, tc.wantStatus)
			}
			assertOutcomeSelfConsistent(t, got)

			// Determinism: flipping only the Ambiguous flag must not change the
			// classification.
			flipped := *tc.err
			flipped.Ambiguous = !flipped.Ambiguous
			if other := classifyUpstream(&flipped); other != got {
				t.Fatalf("Ambiguous flag changed the outcome: %+v vs %+v", got, other)
			}
		})
	}
}

// assertOutcomeSelfConsistent enforces the structural invariant that the receipt
// status, event type and HTTP status always agree with each other.
func assertOutcomeSelfConsistent(t *testing.T, got upstreamOutcome) {
	t.Helper()
	switch got.ReceiptStatus {
	case receiptStatusUnknown:
		if got.EventType != state.EventTypeReceiptUnknown {
			t.Fatalf("unknown status paired with event %q", got.EventType)
		}
		if got.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("unknown status paired with HTTP %d", got.HTTPStatus)
		}
	case receiptStatusFailed:
		if got.EventType != state.EventTypeReceiptFailed {
			t.Fatalf("failed status paired with event %q", got.EventType)
		}
		if got.HTTPStatus != http.StatusUnprocessableEntity && got.HTTPStatus != http.StatusBadGateway {
			t.Fatalf("failed status paired with unexpected HTTP %d", got.HTTPStatus)
		}
	default:
		t.Fatalf("unexpected receipt status %q", got.ReceiptStatus)
	}
}
