package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/lknpd"
	"github.com/ryabkov82/vff-fiscal/internal/state"
)

var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)

type lknpdClient interface {
	GetUser(ctx context.Context) (lknpd.UserInfo, error)
	CreateIncome(ctx context.Context, params lknpd.CreateIncomeParams) (lknpd.Receipt, error)
	CancelIncome(ctx context.Context, receiptUUID, comment string, operationTime time.Time) error
}

type Server struct {
	apiKey             string
	defaultServiceName string
	client             lknpdClient
	store              *state.Store
	logger             *slog.Logger
	mux                *http.ServeMux
}

type createReceiptRequest struct {
	ExternalID    string `json:"external_id"`
	Amount        string `json:"amount"`
	ServiceName   string `json:"service_name,omitempty"`
	OperationTime string `json:"operation_time,omitempty"`
}

type cancelReceiptRequest struct {
	Comment       string `json:"comment,omitempty"`
	OperationTime string `json:"operation_time,omitempty"`
}

func New(apiKey, defaultServiceName string, client lknpdClient, store *state.Store, logger *slog.Logger) *Server {
	s := &Server{
		apiKey:             apiKey,
		defaultServiceName: defaultServiceName,
		client:             client,
		store:              store,
		logger:             logger,
		mux:                http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.logging(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.Handle("GET /v1/user", s.authorize(http.HandlerFunc(s.user)))
	s.mux.Handle("POST /v1/receipts", s.authorize(http.HandlerFunc(s.createReceipt)))
	s.mux.Handle("GET /v1/receipts/{externalID}", s.authorize(http.HandlerFunc(s.getReceipt)))
	s.mux.Handle("POST /v1/receipts/{externalID}/cancel", s.authorize(http.HandlerFunc(s.cancelReceipt)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	user, err := s.client.GetUser(r.Context())
	if err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) createReceipt(w http.ResponseWriter, r *http.Request) {
	var request createReceiptRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.ExternalID = strings.TrimSpace(request.ExternalID)
	request.Amount = strings.TrimSpace(request.Amount)
	request.ServiceName = strings.TrimSpace(request.ServiceName)

	if request.ExternalID == "" || len(request.ExternalID) > 128 {
		writeError(w, http.StatusBadRequest, "external_id is required and must be at most 128 characters")
		return
	}
	if !moneyPattern.MatchString(request.Amount) || request.Amount == "0" || request.Amount == "0.0" || request.Amount == "0.00" {
		writeError(w, http.StatusBadRequest, "amount must be a positive decimal with at most two fractional digits")
		return
	}
	if request.ServiceName == "" {
		request.ServiceName = s.defaultServiceName
	}
	if len(request.ServiceName) > 512 {
		writeError(w, http.StatusBadRequest, "service_name is too long")
		return
	}

	operationTime, err := parseOptionalTime(request.OperationTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "operation_time must be RFC3339")
		return
	}
	if operationTime.IsZero() {
		operationTime = time.Now()
	}

	now := time.Now().UTC()
	record := state.ReceiptRecord{
		ExternalID:    request.ExternalID,
		Amount:        request.Amount,
		ServiceName:   request.ServiceName,
		OperationTime: operationTime,
		Status:        "creating",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	existing, created, err := s.store.ReserveReceipt(record)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist receipt request")
		return
	}
	if !created {
		operationTimeExplicit := strings.TrimSpace(request.OperationTime) != ""
		if !receiptPayloadMatches(existing, request.Amount, request.ServiceName, operationTime, operationTimeExplicit) {
			writeError(w, http.StatusConflict, "external_id already exists with different receipt data")
			return
		}
		status := http.StatusConflict
		if existing.Status == "created" || existing.Status == "cancelled" {
			status = http.StatusOK
		}
		writeJSON(w, status, existing)
		return
	}
	record = existing

	receipt, err := s.client.CreateIncome(r.Context(), lknpd.CreateIncomeParams{
		Amount:        request.Amount,
		ServiceName:   request.ServiceName,
		OperationTime: operationTime,
	})
	if err != nil {
		record.UpdatedAt = time.Now().UTC()
		record.LastError = safeError(err)
		record.Status = "failed"
		var apiErr *lknpd.APIError
		if errors.As(err, &apiErr) && apiErr.Ambiguous {
			record.Status = "unknown"
		}
		_ = s.store.PutReceipt(record)
		s.writeUpstreamError(w, err)
		return
	}

	record.Status = "created"
	record.ReceiptUUID = receipt.ReceiptUUID
	record.PrintURL = receipt.PrintURL
	record.JSONURL = receipt.JSONURL
	record.LastError = ""
	record.UpdatedAt = time.Now().UTC()
	if err := s.store.PutReceipt(record); err != nil {
		s.logger.Error("receipt created at FNS but local state update failed", "external_id", record.ExternalID, "receipt_uuid", record.ReceiptUUID, "error", err)
		writeError(w, http.StatusInternalServerError, "receipt was created but local state could not be updated; manual reconciliation is required")
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func (s *Server) getReceipt(w http.ResponseWriter, r *http.Request) {
	externalID := strings.TrimSpace(r.PathValue("externalID"))
	record, ok := s.store.GetReceipt(externalID)
	if !ok {
		writeError(w, http.StatusNotFound, "receipt not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) cancelReceipt(w http.ResponseWriter, r *http.Request) {
	externalID := strings.TrimSpace(r.PathValue("externalID"))
	record, ok := s.store.GetReceipt(externalID)
	if !ok {
		writeError(w, http.StatusNotFound, "receipt not found")
		return
	}
	if record.Status == "cancelled" {
		writeJSON(w, http.StatusOK, record)
		return
	}
	if record.Status != "created" || record.ReceiptUUID == "" {
		writeError(w, http.StatusConflict, "only a created receipt can be cancelled")
		return
	}

	var request cancelReceiptRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Comment) == "" {
		request.Comment = "Возврат средств"
	}
	operationTime, err := parseOptionalTime(request.OperationTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "operation_time must be RFC3339")
		return
	}

	if err := s.client.CancelIncome(r.Context(), record.ReceiptUUID, request.Comment, operationTime); err != nil {
		s.writeUpstreamError(w, err)
		return
	}
	record.Status = "cancelled"
	record.UpdatedAt = time.Now().UTC()
	record.LastError = ""
	if err := s.store.PutReceipt(record); err != nil {
		writeError(w, http.StatusInternalServerError, "receipt was cancelled but local state could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(provided) != len(s.apiKey) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) writeUpstreamError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var apiErr *lknpd.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status >= 400 && apiErr.Status < 500 {
			status = http.StatusUnprocessableEntity
		}
		if apiErr.Ambiguous {
			status = http.StatusServiceUnavailable
		}
	}
	writeError(w, status, safeError(err))
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func parseOptionalTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func safeError(err error) string {
	message := err.Error()
	if len(message) > 2048 {
		return message[:2048]
	}
	return message
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"status": status, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
