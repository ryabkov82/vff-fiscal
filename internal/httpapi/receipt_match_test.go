package httpapi

import (
	"testing"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func TestNormalizeAmount(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"150", "150.00"},
		{"150.00", "150.00"},
		{"10.5", "10.50"},
		{"0.01", "0.01"},
	}
	for _, tc := range tests {
		got, err := normalizeAmount(tc.in)
		if err != nil {
			t.Fatalf("normalizeAmount(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeAmount(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReceiptPayloadMatchesEquivalentAmounts(t *testing.T) {
	existing := state.ReceiptRecord{
		Amount:      "150.00",
		ServiceName: "Test service",
	}
	if !receiptPayloadMatches(existing, "150", "Test service", time.Time{}, false) {
		t.Fatal("expected equivalent amounts to match")
	}
}

func TestReceiptPayloadMatchesDifferentOperationTime(t *testing.T) {
	first := time.Date(2026, 7, 9, 16, 30, 0, 0, time.FixedZone("MSK", 3*3600))
	second := time.Date(2026, 7, 9, 17, 30, 0, 0, time.FixedZone("MSK", 3*3600))
	existing := state.ReceiptRecord{
		Amount:        "10.00",
		ServiceName:   "Test service",
		OperationTime: first,
	}
	if receiptPayloadMatches(existing, "10.00", "Test service", second, true) {
		t.Fatal("expected different operation times to mismatch")
	}
}
