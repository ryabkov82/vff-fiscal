package state

import "time"

const NotificationDeliveryPending = "pending"

// Semantic notification event types produced when a receipt creation attempt
// leaves the receipt in a non-created terminal-for-notification state.
const (
	EventTypeReceiptFailed  = "receipt.failed"
	EventTypeReceiptUnknown = "receipt.unknown"
)

type NotificationEvent struct {
	SchemaVersion int       `json:"schema_version"`
	EventID       string    `json:"event_id"`
	EventType     string    `json:"event_type"`
	OccurredAt    time.Time `json:"occurred_at"`

	ReceiptExternalID string    `json:"receipt_external_id"`
	Amount            string    `json:"amount,omitempty"`
	OperationTime     time.Time `json:"operation_time,omitempty"`

	ErrorCode string `json:"error_code"`

	DeliveryStatus string     `json:"delivery_status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastResultCode string     `json:"last_result_code,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
}
