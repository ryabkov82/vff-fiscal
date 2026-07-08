package httpapi

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/state"
)

func normalizeAmount(amount string) (string, error) {
	if !moneyPattern.MatchString(amount) {
		return "", fmt.Errorf("invalid amount %q", amount)
	}

	dollarsPart := amount
	cents := 0
	if dot := strings.Index(amount, "."); dot >= 0 {
		dollarsPart = amount[:dot]
		frac := amount[dot+1:]
		switch len(frac) {
		case 1:
			value, err := strconv.Atoi(frac + "0")
			if err != nil {
				return "", err
			}
			cents = value
		case 2:
			value, err := strconv.Atoi(frac)
			if err != nil {
				return "", err
			}
			cents = value
		}
	}

	dollars, err := strconv.Atoi(dollarsPart)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%02d", dollars, cents), nil
}

func receiptPayloadMatches(existing state.ReceiptRecord, amount, serviceName string, operationTime time.Time, operationTimeExplicit bool) bool {
	existingAmount, err := normalizeAmount(existing.Amount)
	if err != nil {
		return false
	}
	incomingAmount, err := normalizeAmount(amount)
	if err != nil || existingAmount != incomingAmount {
		return false
	}
	if existing.ServiceName != serviceName {
		return false
	}
	if operationTimeExplicit && !existing.OperationTime.Equal(operationTime) {
		return false
	}
	return true
}
