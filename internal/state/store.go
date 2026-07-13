package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AuthState struct {
	RefreshToken string `json:"refresh_token"`
	DeviceID     string `json:"device_id"`
	INN          string `json:"inn"`
}

type ReceiptRecord struct {
	ExternalID    string    `json:"external_id"`
	Amount        string    `json:"amount"`
	ServiceName   string    `json:"service_name"`
	OperationTime time.Time `json:"operation_time"`
	Status        string    `json:"status"`
	ReceiptUUID   string    `json:"receipt_uuid,omitempty"`
	PrintURL      string    `json:"print_url,omitempty"`
	JSONURL       string    `json:"json_url,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type fileData struct {
	Version            int                          `json:"version"`
	Auth               AuthState                    `json:"auth"`
	Receipts           map[string]ReceiptRecord     `json:"receipts"`
	NotificationOutbox map[string]NotificationEvent `json:"notification_outbox"`
}

type Store struct {
	mu           sync.Mutex
	path         string
	data         fileData
	syncStateDir func(string) error
}

func Open(path string, initial AuthState) (*Store, error) {
	if path == "" {
		return nil, errors.New("state path is empty")
	}

	s := &Store{path: path, syncStateDir: syncParentDir}

	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(content, &s.data); err != nil {
			return nil, fmt.Errorf("decode state file: %w", err)
		}
		if s.data.Version < 0 {
			return nil, ErrInvalidStateVersion
		}
		if s.data.Version > currentVersion {
			return nil, ErrUnsupportedStateVersion
		}
		if s.data.Version == currentVersion {
			if err := validateOutboxPresence(content); err != nil {
				return nil, err
			}
		}
	case errors.Is(err, os.ErrNotExist):
		s.data = newFileData(initial)
		if _, err := s.persistLocked(s.data); err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, fmt.Errorf("read state file: %w", err)
	}

	changed := normalizeFileData(&s.data)

	if s.data.Version < currentVersion {
		s.data.Version = currentVersion
		changed = true
	}

	if s.data.Auth.RefreshToken == "" && initial.RefreshToken != "" {
		s.data.Auth.RefreshToken = initial.RefreshToken
		changed = true
	}
	if s.data.Auth.DeviceID == "" && initial.DeviceID != "" {
		s.data.Auth.DeviceID = initial.DeviceID
		changed = true
	}
	if s.data.Auth.INN == "" && initial.INN != "" {
		s.data.Auth.INN = initial.INN
		changed = true
	}

	if changed {
		if _, err := s.persistLocked(s.data); err != nil {
			return nil, err
		}
	}

	return s, nil
}

func newFileData(initial AuthState) fileData {
	return fileData{
		Version:            currentVersion,
		Auth:               initial,
		Receipts:           make(map[string]ReceiptRecord),
		NotificationOutbox: make(map[string]NotificationEvent),
	}
}

// validateOutboxPresence enforces that a version 2 state carries an explicit
// notification_outbox object. It must run before normalizeFileData so that a
// corrupted v2 state cannot be silently repaired into an empty outbox.
func validateOutboxPresence(content []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		return fmt.Errorf("decode state file: %w", err)
	}
	value, ok := raw["notification_outbox"]
	if !ok {
		return ErrInvalidNotificationOutbox
	}
	if string(bytes.TrimSpace(value)) == "null" {
		return ErrInvalidNotificationOutbox
	}
	return nil
}

func (s *Store) Auth() AuthState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Auth
}

func (s *Store) UpdateAuth(auth AuthState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(func(candidate *fileData) error {
		candidate.Auth = auth
		return nil
	})
}

func (s *Store) GetReceipt(externalID string) (ReceiptRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.Receipts[externalID]
	return record, ok
}

func (s *Store) PutReceipt(record ReceiptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(func(candidate *fileData) error {
		candidate.Receipts[record.ExternalID] = record
		return nil
	})
}

func (s *Store) ReserveReceipt(record ReceiptRecord) (ReceiptRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data.Receipts[record.ExternalID]; ok {
		return existing, false, nil
	}

	if err := s.mutateLocked(func(candidate *fileData) error {
		candidate.Receipts[record.ExternalID] = record
		return nil
	}); err != nil {
		return ReceiptRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) TransitionReceiptWithEvent(
	externalID string,
	expectedStatus string,
	updated ReceiptRecord,
	event NotificationEvent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(func(candidate *fileData) error {
		existing, ok := candidate.Receipts[externalID]
		if !ok {
			return ErrReceiptNotFound
		}
		if existing.Status != expectedStatus {
			return ErrReceiptStatusMismatch
		}
		if updated.ExternalID != externalID {
			return ErrInvalidTransitionInput
		}
		if event.EventID == "" {
			return ErrInvalidTransitionInput
		}
		if event.ReceiptExternalID != externalID {
			return ErrInvalidTransitionInput
		}
		if _, exists := candidate.NotificationOutbox[event.EventID]; exists {
			return ErrDuplicateNotificationEvent
		}

		candidate.Receipts[externalID] = updated
		candidate.NotificationOutbox[event.EventID] = cloneNotificationEvent(event)
		return nil
	})
}

func (s *Store) GetNotificationEvent(eventID string) (NotificationEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event, ok := s.data.NotificationOutbox[eventID]
	if !ok {
		return NotificationEvent{}, false
	}
	return cloneNotificationEvent(event), true
}

func (s *Store) mutateLocked(mutate func(*fileData) error) error {
	candidate := cloneFileData(s.data)
	normalizeFileData(&candidate)
	if err := mutate(&candidate); err != nil {
		return err
	}
	normalizeFileData(&candidate)

	committed, err := s.persistLocked(candidate)
	if committed {
		s.data = candidate
	}
	return err
}

// persistLocked atomically writes data to disk. The returned committed flag is
// true once the atomic rename has replaced the state file: rename is the
// logical commit point. Any error before rename leaves both disk and the
// committed flag untouched (committed=false). A failure to sync the parent
// directory after rename returns ErrStateDurabilityUncertain with committed
// still true, because the new state is already visible and callers must adopt
// it in memory even though power-loss durability is not guaranteed.
func (s *Store) persistLocked(data fileData) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return false, fmt.Errorf("create state directory: %w", err)
	}

	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode state file: %w", err)
	}
	content = append(content, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temporary state file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod temporary state file: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return false, fmt.Errorf("replace state file: %w", err)
	}
	cleanup = false

	syncDir := s.syncStateDir
	if syncDir == nil {
		syncDir = syncParentDir
	}
	if err := syncDir(s.path); err != nil {
		return true, fmt.Errorf("sync state directory: %w", errors.Join(ErrStateDurabilityUncertain, err))
	}
	return true, nil
}
