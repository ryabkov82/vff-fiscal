package state

import (
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
