package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStorePersistsAuthAndReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh-1", DeviceID: "device-1", INN: "123"})
	if err != nil {
		t.Fatal(err)
	}

	record := ReceiptRecord{
		ExternalID:    "shm:42",
		Amount:        "299.00",
		ServiceName:   "VPN",
		OperationTime: time.Now(),
		Status:        "created",
		ReceiptUUID:   "uuid-1",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := store.PutReceipt(record); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Auth(); got.RefreshToken != "refresh-1" || got.DeviceID != "device-1" {
		t.Fatalf("unexpected auth: %+v", got)
	}
	got, ok := reopened.GetReceipt("shm:42")
	if !ok || got.ReceiptUUID != "uuid-1" {
		t.Fatalf("unexpected receipt: %+v, exists=%v", got, ok)
	}
}

func TestReserveReceiptConcurrentSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var createdCount atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			record := ReceiptRecord{
				ExternalID:    "shm:race",
				Amount:        "100.00",
				ServiceName:   "VPN",
				OperationTime: time.Now(),
				Status:        "creating",
				CreatedAt:     time.Now().UTC(),
				UpdatedAt:     time.Now().UTC(),
			}
			_, created, err := store.ReserveReceipt(record)
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				createdCount.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := createdCount.Load(); got != 1 {
		t.Fatalf("expected exactly one reservation, got %d", got)
	}
	record, ok := store.GetReceipt("shm:race")
	if !ok || record.Status != "creating" {
		t.Fatalf("unexpected reserved receipt: %+v, exists=%v", record, ok)
	}
}

func TestReserveReceiptReturnsExistingWithoutCreating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	initial := ReceiptRecord{
		ExternalID:    "shm:1",
		Amount:        "50.00",
		ServiceName:   "VPN",
		OperationTime: time.Now(),
		Status:        "creating",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	_, created, err := store.ReserveReceipt(initial)
	if err != nil || !created {
		t.Fatalf("first reserve: created=%v err=%v", created, err)
	}

	existing, created, err := store.ReserveReceipt(ReceiptRecord{
		ExternalID: "shm:1",
		Status:     "creating",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second reserve must not create a new record")
	}
	if existing.ExternalID != "shm:1" || existing.Amount != "50.00" {
		t.Fatalf("unexpected existing record: %+v", existing)
	}
}

func writeStateFile(t *testing.T, path string, payload any) {
	t.Helper()
	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStateFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOpenMigratesStateV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	writeStateFile(t, path, map[string]any{
		"version": 1,
		"auth": map[string]string{
			"refresh_token": "refresh-v1",
			"device_id":     "device-v1",
			"inn":           "7700000000",
		},
		"receipts": map[string]any{
			"shm:1": map[string]any{
				"external_id":    "shm:1",
				"amount":         "100.00",
				"service_name":   "VPN",
				"operation_time": now,
				"status":         "creating",
				"created_at":     now,
				"updated_at":     now,
			},
		},
	})

	store, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}

	auth := store.Auth()
	if auth.RefreshToken != "refresh-v1" || auth.DeviceID != "device-v1" || auth.INN != "7700000000" {
		t.Fatalf("unexpected auth after migration: %+v", auth)
	}
	receipt, ok := store.GetReceipt("shm:1")
	if !ok || receipt.Status != "creating" || receipt.Amount != "100.00" {
		t.Fatalf("unexpected receipt after migration: %+v, ok=%v", receipt, ok)
	}
	if _, ok := store.GetNotificationEvent("any"); ok {
		t.Fatal("outbox must be empty after migration")
	}

	raw := readStateFile(t, path)
	if string(raw["version"]) != "2" {
		t.Fatalf("expected version 2 on disk, got %s", raw["version"])
	}
	if string(raw["notification_outbox"]) != "{}" {
		t.Fatalf("expected empty notification_outbox object, got %s", raw["notification_outbox"])
	}
}

func TestOpenLegacyMissingVersionMigratesToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, path, map[string]any{
		"auth": map[string]string{
			"refresh_token": "refresh-legacy",
			"device_id":     "device-legacy",
			"inn":           "7700000001",
		},
		"receipts": map[string]any{},
	})

	store, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	if store.Auth().RefreshToken != "refresh-legacy" {
		t.Fatalf("unexpected auth: %+v", store.Auth())
	}
	raw := readStateFile(t, path)
	if string(raw["version"]) != "2" {
		t.Fatalf("expected version 2 on disk, got %s", raw["version"])
	}
}

func TestOpenReopensMigratedStateV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	writeStateFile(t, path, map[string]any{
		"version": 1,
		"auth": map[string]string{
			"refresh_token": "refresh-v1",
			"device_id":     "device-v1",
			"inn":           "7700000000",
		},
		"receipts": map[string]any{
			"shm:1": map[string]any{
				"external_id":    "shm:1",
				"amount":         "100.00",
				"service_name":   "VPN",
				"operation_time": now,
				"status":         "creating",
				"created_at":     now,
				"updated_at":     now,
			},
		},
	})

	first, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.GetReceipt("shm:1"); !ok {
		t.Fatal("expected receipt after first open")
	}

	second, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := second.GetReceipt("shm:1")
	if !ok || receipt.Amount != "100.00" {
		t.Fatalf("unexpected receipt after second open: %+v, ok=%v", receipt, ok)
	}
	if second.Auth().RefreshToken != "refresh-v1" {
		t.Fatalf("unexpected auth after second open: %+v", second.Auth())
	}
	raw := readStateFile(t, path)
	if string(raw["version"]) != "2" {
		t.Fatalf("expected version 2 on disk, got %s", raw["version"])
	}
}

func TestOpenRejectsUnsupportedFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{
  "version": 3,
  "auth": {
    "refresh_token": "refresh-v3",
    "device_id": "device-v3",
    "inn": "7700000000"
  },
  "receipts": {}
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path, AuthState{})
	if !errors.Is(err, ErrUnsupportedStateVersion) {
		t.Fatalf("expected ErrUnsupportedStateVersion, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatal("unsupported version must not modify state file")
	}
}

func sampleCreatingReceipt(externalID string) ReceiptRecord {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	return ReceiptRecord{
		ExternalID:    externalID,
		Amount:        "100.00",
		ServiceName:   "VPN",
		OperationTime: now,
		Status:        "creating",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func sampleNotificationEvent(eventID, externalID string) NotificationEvent {
	now := time.Date(2026, 7, 13, 12, 1, 0, 0, time.UTC)
	return NotificationEvent{
		SchemaVersion:     1,
		EventID:           eventID,
		EventType:         "receipt.failed",
		OccurredAt:        now,
		ReceiptExternalID: externalID,
		Amount:            "100.00",
		ErrorCode:         "upstream_timeout",
		DeliveryStatus:    NotificationDeliveryPending,
		NextAttemptAt:     now,
	}
}

func TestTransitionReceiptWithEventSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:transition")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}

	updated := initial
	updated.Status = "failed"
	updated.LastError = "timeout"
	updated.UpdatedAt = time.Date(2026, 7, 13, 12, 2, 0, 0, time.UTC)
	event := sampleNotificationEvent("evt-1", "shm:transition")

	if err := store.TransitionReceiptWithEvent("shm:transition", "creating", updated, event); err != nil {
		t.Fatal(err)
	}

	gotReceipt, ok := store.GetReceipt("shm:transition")
	if !ok || gotReceipt.Status != "failed" {
		t.Fatalf("unexpected receipt in memory: %+v, ok=%v", gotReceipt, ok)
	}
	gotEvent, ok := store.GetNotificationEvent("evt-1")
	if !ok || gotEvent.EventType != "receipt.failed" {
		t.Fatalf("unexpected event in memory: %+v, ok=%v", gotEvent, ok)
	}

	reopened, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	persistedReceipt, ok := reopened.GetReceipt("shm:transition")
	if !ok || persistedReceipt.Status != "failed" {
		t.Fatalf("unexpected persisted receipt: %+v, ok=%v", persistedReceipt, ok)
	}
	persistedEvent, ok := reopened.GetNotificationEvent("evt-1")
	if !ok || persistedEvent.DeliveryStatus != NotificationDeliveryPending {
		t.Fatalf("unexpected persisted event: %+v, ok=%v", persistedEvent, ok)
	}
}

func TestTransitionReceiptWithEventStatusMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:mismatch")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	updated := initial
	updated.Status = "failed"
	event := sampleNotificationEvent("evt-mismatch", "shm:mismatch")

	err = store.TransitionReceiptWithEvent("shm:mismatch", "created", updated, event)
	if !errors.Is(err, ErrReceiptStatusMismatch) {
		t.Fatalf("expected ErrReceiptStatusMismatch, got %v", err)
	}

	got, ok := store.GetReceipt("shm:mismatch")
	if !ok || got.Status != "creating" {
		t.Fatalf("receipt must remain unchanged: %+v, ok=%v", got, ok)
	}
	if _, ok := store.GetNotificationEvent("evt-mismatch"); ok {
		t.Fatal("event must not be added on status mismatch")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("state file must remain unchanged on status mismatch")
	}
}

func TestTransitionReceiptWithEventDuplicateEventID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:dup")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}

	updated := initial
	updated.Status = "failed"
	event := sampleNotificationEvent("evt-dup", "shm:dup")
	if err := store.TransitionReceiptWithEvent("shm:dup", "creating", updated, event); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	updatedAgain := updated
	updatedAgain.LastError = "other"
	err = store.TransitionReceiptWithEvent("shm:dup", "failed", updatedAgain, event)
	if !errors.Is(err, ErrDuplicateNotificationEvent) {
		t.Fatalf("expected ErrDuplicateNotificationEvent, got %v", err)
	}

	got, ok := store.GetReceipt("shm:dup")
	if !ok || got.LastError != "" {
		t.Fatalf("receipt must remain unchanged after duplicate event: %+v, ok=%v", got, ok)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("state file must remain unchanged on duplicate event")
	}
}

func badPersistPath(baseDir string) string {
	blockingFile := filepath.Join(baseDir, "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		panic(err)
	}
	return filepath.Join(blockingFile, "state.json")
}

func TestTransitionReceiptWithEventRollbackOnPersistFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:rollback")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	store.path = badPersistPath(t.TempDir())

	updated := initial
	updated.Status = "failed"
	event := sampleNotificationEvent("evt-rollback", "shm:rollback")
	if err := store.TransitionReceiptWithEvent("shm:rollback", "creating", updated, event); err == nil {
		t.Fatal("expected persist failure")
	}

	got, ok := store.GetReceipt("shm:rollback")
	if !ok || got.Status != "creating" {
		t.Fatalf("receipt must remain unchanged in memory: %+v, ok=%v", got, ok)
	}
	if _, ok := store.GetNotificationEvent("evt-rollback"); ok {
		t.Fatal("event must not remain in memory after persist failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("original state file must remain unchanged after persist failure")
	}
}

func TestMutationsRollbackOnPersistFailure(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, store *Store) error
	}{
		{
			name: "PutReceipt",
			run: func(t *testing.T, store *Store) error {
				record := sampleCreatingReceipt("shm:put-rollback")
				return store.PutReceipt(record)
			},
		},
		{
			name: "UpdateAuth",
			run: func(_ *testing.T, store *Store) error {
				return store.UpdateAuth(AuthState{
					RefreshToken: "new-refresh",
					DeviceID:     "new-device",
					INN:          "7700000099",
				})
			},
		},
		{
			name: "ReserveReceipt",
			run: func(_ *testing.T, store *Store) error {
				_, _, err := store.ReserveReceipt(sampleCreatingReceipt("shm:reserve-rollback"))
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
			if err != nil {
				t.Fatal(err)
			}
			beforeAuth := store.Auth()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			store.path = badPersistPath(t.TempDir())
			if err := tc.run(t, store); err == nil {
				t.Fatal("expected persist failure")
			}

			if got := store.Auth(); got != beforeAuth {
				t.Fatalf("auth must remain unchanged in memory: before=%+v after=%+v", beforeAuth, got)
			}
			switch tc.name {
			case "PutReceipt":
				if _, ok := store.GetReceipt("shm:put-rollback"); ok {
					t.Fatal("PutReceipt must not remain in memory after persist failure")
				}
			case "ReserveReceipt":
				if _, ok := store.GetReceipt("shm:reserve-rollback"); ok {
					t.Fatal("ReserveReceipt must not remain in memory after persist failure")
				}
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("original state file must remain unchanged after persist failure")
			}
		})
	}
}

func TestTransitionReceiptWithEventReceiptNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	updated := sampleCreatingReceipt("missing")
	event := sampleNotificationEvent("evt-missing", "missing")
	err = store.TransitionReceiptWithEvent("missing", "creating", updated, event)
	if !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("expected ErrReceiptNotFound, got %v", err)
	}
}

func TestTransitionReceiptWithEventInvalidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:invalid")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}

	updated := initial
	updated.Status = "failed"
	event := sampleNotificationEvent("evt-invalid", "other-id")

	err = store.TransitionReceiptWithEvent("shm:invalid", "creating", updated, event)
	if !errors.Is(err, ErrInvalidTransitionInput) {
		t.Fatalf("expected ErrInvalidTransitionInput, got %v", err)
	}
}

func TestNewStorePersistsEmptyNotificationOutboxObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	_, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	raw := readStateFile(t, path)
	if string(raw["notification_outbox"]) != "{}" {
		t.Fatalf("expected notification_outbox to be {}, got %s", raw["notification_outbox"])
	}
}

var errTestSyncDir = errors.New("test sync dir failure")

func TestMutationDurabilityUncertainAfterRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	store.syncStateDir = func(string) error { return errTestSyncDir }

	record := sampleCreatingReceipt("shm:durability")
	err = store.PutReceipt(record)
	if err == nil {
		t.Fatal("expected durability error")
	}
	if !errors.Is(err, ErrStateDurabilityUncertain) {
		t.Fatalf("expected ErrStateDurabilityUncertain, got %v", err)
	}

	got, ok := store.GetReceipt("shm:durability")
	if !ok || got.Status != "creating" {
		t.Fatalf("committed record must be present in memory: %+v, ok=%v", got, ok)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected state file mode 0600, got %o", perm)
	}

	reopened, err := Open(path, AuthState{})
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.GetReceipt("shm:durability")
	if !ok || persisted.Status != "creating" {
		t.Fatalf("committed record must be present on disk: %+v, ok=%v", persisted, ok)
	}
}

func TestOpenRejectsNegativeVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{
  "version": -1,
  "auth": {
    "refresh_token": "refresh-neg",
    "device_id": "device-neg",
    "inn": "7700000000"
  },
  "receipts": {}
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path, AuthState{})
	if !errors.Is(err, ErrInvalidStateVersion) {
		t.Fatalf("expected ErrInvalidStateVersion, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatal("negative version must not modify state file")
	}
}

func TestOpenRejectsV2WithoutOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{
  "version": 2,
  "auth": {
    "refresh_token": "refresh-v2",
    "device_id": "device-v2",
    "inn": "7700000000"
  },
  "receipts": {}
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path, AuthState{})
	if !errors.Is(err, ErrInvalidNotificationOutbox) {
		t.Fatalf("expected ErrInvalidNotificationOutbox, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatal("corrupted v2 state must not be modified")
	}
}

func TestOpenRejectsV2WithNullOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := `{
  "version": 2,
  "auth": {
    "refresh_token": "refresh-v2",
    "device_id": "device-v2",
    "inn": "7700000000"
  },
  "receipts": {},
  "notification_outbox": null
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path, AuthState{})
	if !errors.Is(err, ErrInvalidNotificationOutbox) {
		t.Fatalf("expected ErrInvalidNotificationOutbox, got %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatal("v2 state with null outbox must not be modified")
	}
}

func TestOpenV1WithoutOutboxStillMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, path, map[string]any{
		"version": 1,
		"auth": map[string]string{
			"refresh_token": "refresh-v1",
			"device_id":     "device-v1",
			"inn":           "7700000000",
		},
		"receipts": map[string]any{},
	})

	store, err := Open(path, AuthState{})
	if err != nil {
		t.Fatalf("v1 without outbox must migrate successfully, got %v", err)
	}
	if store.Auth().RefreshToken != "refresh-v1" {
		t.Fatalf("unexpected auth: %+v", store.Auth())
	}
	raw := readStateFile(t, path)
	if string(raw["version"]) != "2" {
		t.Fatalf("expected version 2 on disk, got %s", raw["version"])
	}
	if string(raw["notification_outbox"]) != "{}" {
		t.Fatalf("expected notification_outbox to be {}, got %s", raw["notification_outbox"])
	}
}

func TestNotificationEventNoPointerAliasing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := Open(path, AuthState{RefreshToken: "refresh", DeviceID: "device", INN: "7700000000"})
	if err != nil {
		t.Fatal(err)
	}

	initial := sampleCreatingReceipt("shm:alias")
	if err := store.PutReceipt(initial); err != nil {
		t.Fatal(err)
	}

	updated := initial
	updated.Status = "failed"

	wantLastAttempt := time.Date(2026, 7, 13, 12, 5, 0, 0, time.UTC)
	wantDelivered := time.Date(2026, 7, 13, 12, 6, 0, 0, time.UTC)
	lastAttempt := wantLastAttempt
	delivered := wantDelivered
	event := sampleNotificationEvent("evt-alias", "shm:alias")
	event.LastAttemptAt = &lastAttempt
	event.DeliveredAt = &delivered

	if err := store.TransitionReceiptWithEvent("shm:alias", "creating", updated, event); err != nil {
		t.Fatal(err)
	}

	// Mutate caller-owned pointers after write; Store must not observe changes.
	*event.LastAttemptAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	*event.DeliveredAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	stored, ok := store.GetNotificationEvent("evt-alias")
	if !ok {
		t.Fatal("event must exist")
	}
	if !stored.LastAttemptAt.Equal(wantLastAttempt) {
		t.Fatalf("LastAttemptAt aliased caller pointer: got %v want %v", stored.LastAttemptAt, wantLastAttempt)
	}
	if !stored.DeliveredAt.Equal(wantDelivered) {
		t.Fatalf("DeliveredAt aliased caller pointer: got %v want %v", stored.DeliveredAt, wantDelivered)
	}

	// Mutate pointers returned by GetNotificationEvent; Store must not change.
	*stored.LastAttemptAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	*stored.DeliveredAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)

	again, ok := store.GetNotificationEvent("evt-alias")
	if !ok {
		t.Fatal("event must still exist")
	}
	if !again.LastAttemptAt.Equal(wantLastAttempt) {
		t.Fatalf("GetNotificationEvent leaked internal pointer for LastAttemptAt: got %v", again.LastAttemptAt)
	}
	if !again.DeliveredAt.Equal(wantDelivered) {
		t.Fatalf("GetNotificationEvent leaked internal pointer for DeliveredAt: got %v", again.DeliveredAt)
	}
}
