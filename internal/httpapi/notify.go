package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

const notificationSchemaVersion = 1

// notificationEventIDVersion is embedded in the canonical hashed string so the
// event-ID derivation can evolve without silently colliding with historical
// ids. It must be bumped whenever the canonical layout below changes.
const notificationEventIDVersion = "v1"

// Receipt status values produced for a failed creation attempt.
const (
	receiptStatusFailed  = "failed"  // definitely not registered upstream
	receiptStatusUnknown = "unknown" // may or may not be registered upstream
)

// Closed set of safe error codes. These are the only failure descriptors that
// ever reach ReceiptRecord.LastError, the notification outbox, or the HTTP
// response body. They never contain raw upstream text, response bodies, INN,
// tokens, URLs, or stack traces.
const (
	errCodeUpstreamTimeout    = "upstream_timeout"          // request sent, timed out
	errCodeUpstreamTransport  = "upstream_transport"        // request sent, transport failure
	errCodeUpstream5xx        = "upstream_5xx"              // upstream server error
	errCodeInvalidResponse    = "upstream_invalid_response" // 2xx without approvedReceiptUuid
	errCodeAuthRejected       = "auth_rejected"             // 401/403
	errCodeRateLimited        = "rate_limited"              // 429
	errCodeValidationRejected = "validation_rejected"       // 400/422
	errCodeRequestRejected    = "request_rejected"          // other 4xx
	errCodeUpstreamError      = "upstream_error"            // generic / non-classified
)

// upstreamOutcome is the single, self-consistent classification of an upstream
// error. Deriving the safe error code, receipt status, event type and HTTP
// status together from one place makes contradictory pairings (for example
// "unknown"+auth_rejected or "failed"+upstream_5xx) structurally impossible.
type upstreamOutcome struct {
	Code          string // safe error code, never carries upstream text
	ReceiptStatus string // receiptStatusFailed or receiptStatusUnknown
	EventType     string // notification event type matching ReceiptStatus
	HTTPStatus    int    // recommended HTTP status for the client response
}

// classifyUpstream maps an upstream error onto a single consistent outcome using
// only structured signals (typed error, sentinel, HTTP status) and never by
// inspecting err.Error() text or any upstream response body.
//
// Ambiguity (whether the receipt might have been registered upstream) is decided
// by the error category itself, not by the raw APIError.Ambiguous flag, so even
// a contradictory APIError value cannot produce an inconsistent result:
//   - timeout, transport failure after send, 5xx and an empty approved UUID are
//     ambiguous -> "unknown";
//   - confirmed 4xx rejections are definite -> "failed".
func classifyUpstream(err error) upstreamOutcome {
	code := upstreamErrorCode(err)
	switch code {
	case errCodeUpstreamTimeout, errCodeUpstreamTransport, errCodeUpstream5xx, errCodeInvalidResponse:
		return upstreamOutcome{
			Code:          code,
			ReceiptStatus: receiptStatusUnknown,
			EventType:     state.EventTypeReceiptUnknown,
			HTTPStatus:    http.StatusServiceUnavailable,
		}
	case errCodeAuthRejected, errCodeRateLimited, errCodeValidationRejected, errCodeRequestRejected:
		return upstreamOutcome{
			Code:          code,
			ReceiptStatus: receiptStatusFailed,
			EventType:     state.EventTypeReceiptFailed,
			HTTPStatus:    http.StatusUnprocessableEntity,
		}
	default:
		// errCodeUpstreamError: the request never reached a definite send
		// (missing config, token failure, request-build error). Treat it as a
		// definite failure and surface a gateway error.
		return upstreamOutcome{
			Code:          errCodeUpstreamError,
			ReceiptStatus: receiptStatusFailed,
			EventType:     state.EventTypeReceiptFailed,
			HTTPStatus:    http.StatusBadGateway,
		}
	}
}

// upstreamErrorCode maps an upstream error onto the closed error-code set using
// only structured signals, never by parsing err.Error() text or upstream body.
func upstreamErrorCode(err error) string {
	var apiErr *lknpd.APIError
	if !errors.As(err, &apiErr) {
		return errCodeUpstreamError
	}

	if errors.Is(apiErr.Err, lknpd.ErrEmptyApprovedReceiptUUID) {
		return errCodeInvalidResponse
	}

	if apiErr.Status == 0 {
		if isTimeout(apiErr.Err) {
			return errCodeUpstreamTimeout
		}
		return errCodeUpstreamTransport
	}

	switch {
	case apiErr.Status == http.StatusTooManyRequests:
		return errCodeRateLimited
	case apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden:
		return errCodeAuthRejected
	case apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnprocessableEntity:
		return errCodeValidationRejected
	case apiErr.Status >= 500:
		return errCodeUpstream5xx
	case apiErr.Status >= 400:
		return errCodeRequestRejected
	default:
		return errCodeUpstreamError
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// notificationEventID derives a deterministic identifier from a versioned
// canonical string "v1\0<event_type>\0<external_id>". It is stable across
// retries (so a repeated attempt maps to the same outbox entry), differs per
// event type, and never embeds the plaintext external ID. The result is always
// "evt_" followed by 64 lowercase hex characters.
func notificationEventID(externalID, eventType string) string {
	canonical := notificationEventIDVersion + "\x00" + eventType + "\x00" + externalID
	sum := sha256.Sum256([]byte(canonical))
	return "evt_" + hex.EncodeToString(sum[:])
}

// buildFailureEvent assembles a notification event carrying only safe fields.
func buildFailureEvent(record state.ReceiptRecord, eventType, errorCode string, occurredAt time.Time) state.NotificationEvent {
	return state.NotificationEvent{
		SchemaVersion:     notificationSchemaVersion,
		EventID:           notificationEventID(record.ExternalID, eventType),
		EventType:         eventType,
		OccurredAt:        occurredAt,
		ReceiptExternalID: record.ExternalID,
		Amount:            record.Amount,
		OperationTime:     record.OperationTime,
		ErrorCode:         errorCode,
		DeliveryStatus:    state.NotificationDeliveryPending,
		Attempts:          0,
		NextAttemptAt:     occurredAt,
	}
}
