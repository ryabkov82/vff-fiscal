package state

import "errors"

const currentVersion = 2

var (
	ErrReceiptNotFound            = errors.New("receipt not found")
	ErrReceiptStatusMismatch      = errors.New("receipt status mismatch")
	ErrDuplicateNotificationEvent = errors.New("duplicate notification event")
	ErrInvalidTransitionInput     = errors.New("invalid transition input")
	ErrUnsupportedStateVersion    = errors.New("unsupported state version")
	ErrInvalidStateVersion        = errors.New("invalid state version")
	ErrInvalidNotificationOutbox  = errors.New("invalid notification outbox")
	ErrStateDurabilityUncertain   = errors.New("state durability uncertain after commit")
)
