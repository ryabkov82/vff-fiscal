package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const currentVersion = 1

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
	Version  int                      `json:"version"`
	Auth     AuthState                `json:"auth"`
	Receipts map[string]ReceiptRecord `json:"receipts"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data fileData
}

func Open(path string, initial AuthState) (*Store, error) {
	if path == "" {
		return nil, errors.New("state path is empty")
	}

	s := &Store{
		path: path,
		data: fileData{Version: currentVersion, Receipts: make(map[string]ReceiptRecord)},
	}

	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(content, &s.data); err != nil {
			return nil, fmt.Errorf("decode state file: %w", err)
		}
		if s.data.Receipts == nil {
			s.data.Receipts = make(map[string]ReceiptRecord)
		}
	case errors.Is(err, os.ErrNotExist):
		s.data.Auth = initial
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("read state file: %w", err)
	}

	changed := false
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
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}

	return s, nil
}

func (s *Store) Auth() AuthState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Auth
}

func (s *Store) UpdateAuth(auth AuthState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Auth = auth
	return s.saveLocked()
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
	if s.data.Receipts == nil {
		s.data.Receipts = make(map[string]ReceiptRecord)
	}
	s.data.Receipts[record.ExternalID] = record
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	content, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}
	content = append(content, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temporary state file: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}
