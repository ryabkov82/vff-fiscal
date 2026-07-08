package state

import (
	"path/filepath"
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
