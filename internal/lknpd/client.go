package lknpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryabkov82/vff-fiscal/internal/state"
)

const maxResponseBody = 1 << 20

// ErrEmptyApprovedReceiptUUID marks a successful /income HTTP response that did
// not carry an approvedReceiptUuid. The receipt may or may not have been
// registered upstream, so callers must treat it as ambiguous.
var ErrEmptyApprovedReceiptUUID = errors.New("lknpd returned an empty approvedReceiptUuid")

type Config struct {
	BaseURL        string
	UserAgent      string
	TimezoneOffset string
	PaymentType    string
	Timeout        time.Duration
}

type Client struct {
	cfg        Config
	httpClient *http.Client
	store      *state.Store

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

type DeviceInfo struct {
	SourceDeviceID string      `json:"sourceDeviceId"`
	SourceType     string      `json:"sourceType"`
	AppVersion     string      `json:"appVersion"`
	MetaDetails    MetaDetails `json:"metaDetails"`
}

type MetaDetails struct {
	UserAgent string `json:"userAgent"`
}

type APIError struct {
	Operation string
	Status    int
	Body      string
	Ambiguous bool
	Err       error
}

// Error renders a safe, stable description of the failure. It deliberately never
// includes the raw upstream Body nor the text of the wrapped e.Err, both of
// which may carry sensitive data (INN, tokens, URLs, dialed hosts, upstream
// response text). Only the operation and, when present, the HTTP status are
// reported. The Body field and the wrapped error remain available for
// in-process, programmatic use only (Unwrap, errors.Is/As) and must never be
// logged or serialized.
func (e *APIError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: upstream responded with HTTP %d", e.Operation, e.Status)
	}
	return fmt.Sprintf("%s: upstream request failed", e.Operation)
}

func (e *APIError) Unwrap() error { return e.Err }

type TokenResponse struct {
	Token         string `json:"token"`
	RefreshToken  string `json:"refreshToken"`
	TokenExpireIn string `json:"tokenExpireIn"`
	Message       string `json:"message"`
	Profile       struct {
		INN string `json:"inn"`
	} `json:"profile"`
}

type UserInfo map[string]any

type CreateIncomeParams struct {
	Amount        string
	ServiceName   string
	OperationTime time.Time
}

type Receipt struct {
	ReceiptUUID string `json:"receipt_uuid"`
	PrintURL    string `json:"print_url"`
	JSONURL     string `json:"json_url"`
}

type incomeResponse struct {
	ApprovedReceiptUUID string `json:"approvedReceiptUuid"`
}

func New(cfg Config, store *state.Store) *Client {
	return &Client{
		cfg:        cfg,
		store:      store,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *Client) GetUser(ctx context.Context) (UserInfo, error) {
	var result UserInfo
	if err := c.authorizedJSON(ctx, http.MethodGet, "/user", nil, &result, false); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) CreateIncome(ctx context.Context, params CreateIncomeParams) (Receipt, error) {
	auth := c.store.Auth()
	if auth.INN == "" {
		return Receipt{}, errors.New("lknpd INN is not configured")
	}

	operationTime := params.OperationTime
	if operationTime.IsZero() {
		operationTime = time.Now()
	}

	requestBody := map[string]any{
		"operationTime":                   c.formatTime(operationTime),
		"requestTime":                     c.formatTime(time.Now()),
		"paymentType":                     c.cfg.PaymentType,
		"ignoreMaxTotalIncomeRestriction": false,
		"client": map[string]any{
			"incomeType":   "FROM_INDIVIDUAL",
			"displayName":  nil,
			"contactPhone": nil,
			"inn":          nil,
		},
		"services": []map[string]any{{
			"name":     params.ServiceName,
			"amount":   params.Amount,
			"quantity": 1,
		}},
		"totalAmount": params.Amount,
	}

	var response incomeResponse
	if err := c.authorizedJSON(ctx, http.MethodPost, "/income", requestBody, &response, true); err != nil {
		return Receipt{}, err
	}
	if response.ApprovedReceiptUUID == "" {
		return Receipt{}, &APIError{Operation: "POST /income", Ambiguous: true, Err: ErrEmptyApprovedReceiptUUID}
	}

	base := strings.TrimRight(c.cfg.BaseURL, "/")
	return Receipt{
		ReceiptUUID: response.ApprovedReceiptUUID,
		PrintURL:    fmt.Sprintf("%s/receipt/%s/%s/print", base, auth.INN, response.ApprovedReceiptUUID),
		JSONURL:     fmt.Sprintf("%s/receipt/%s/%s/json", base, auth.INN, response.ApprovedReceiptUUID),
	}, nil
}

func (c *Client) CancelIncome(ctx context.Context, receiptUUID, comment string, operationTime time.Time) error {
	if operationTime.IsZero() {
		operationTime = time.Now()
	}
	requestBody := map[string]any{
		"receiptUuid":   receiptUUID,
		"comment":       comment,
		"operationTime": c.formatTime(operationTime),
		"requestTime":   c.formatTime(time.Now()),
		"partnerCode":   nil,
	}
	var response map[string]any
	return c.authorizedJSON(ctx, http.MethodPost, "/cancel", requestBody, &response, true)
}

func (c *Client) authorizedJSON(ctx context.Context, method, path string, requestBody, responseBody any, ambiguousOnNetwork bool) error {
	token, err := c.accessTokenFor(ctx)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, method, path, requestBody, responseBody, token, ambiguousOnNetwork)
}

func (c *Client) accessTokenFor(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Add(5*time.Minute).Before(c.expiresAt) {
		return c.accessToken, nil
	}

	auth := c.store.Auth()
	if auth.RefreshToken == "" || auth.DeviceID == "" {
		return "", errors.New("lknpd refresh token and device ID are required")
	}

	requestBody := map[string]any{
		"refreshToken": auth.RefreshToken,
		"deviceInfo":   c.deviceInfo(auth.DeviceID),
	}
	var response TokenResponse
	if err := c.doJSON(ctx, http.MethodPost, "/auth/token", requestBody, &response, "", false); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("lknpd token response has no token: %s", response.Message)
	}

	if response.RefreshToken != "" {
		auth.RefreshToken = response.RefreshToken
	}
	if response.Profile.INN != "" {
		auth.INN = response.Profile.INN
	}
	if err := c.store.UpdateAuth(auth); err != nil {
		return "", fmt.Errorf("persist rotated lknpd token: %w", err)
	}

	c.accessToken = response.Token
	if parsed, err := time.Parse(time.RFC3339Nano, response.TokenExpireIn); err == nil {
		c.expiresAt = parsed
	} else {
		c.expiresAt = time.Now().Add(10 * time.Minute)
	}
	return c.accessToken, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody, responseBody any, bearer string, ambiguousOnNetwork bool) error {
	var body io.Reader
	if requestBody != nil {
		content, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(content)
	}

	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.BaseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", c.cfg.UserAgent)
	request.Header.Set("Referer", "https://lknpd.nalog.ru/")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return &APIError{Operation: method + " " + path, Ambiguous: ambiguousOnNetwork, Err: err}
	}
	defer response.Body.Close()

	content, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return &APIError{Operation: method + " " + path, Status: response.StatusCode, Ambiguous: ambiguousOnNetwork, Err: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{
			Operation: method + " " + path,
			Status:    response.StatusCode,
			Body:      strings.TrimSpace(string(content)),
			Ambiguous: response.StatusCode >= 500 && ambiguousOnNetwork,
		}
	}
	if responseBody == nil || len(content) == 0 {
		return nil
	}
	if err := json.Unmarshal(content, responseBody); err != nil {
		return &APIError{Operation: method + " " + path, Status: response.StatusCode, Body: "invalid JSON response", Ambiguous: ambiguousOnNetwork, Err: err}
	}
	return nil
}

func (c *Client) deviceInfo(deviceID string) DeviceInfo {
	return DeviceInfo{
		SourceDeviceID: deviceID,
		SourceType:     "WEB",
		AppVersion:     "1.0.0",
		MetaDetails:    MetaDetails{UserAgent: c.cfg.UserAgent},
	}
}

func (c *Client) formatTime(value time.Time) string {
	offset := c.cfg.TimezoneOffset
	sign := 1
	if offset[0] == '-' {
		sign = -1
	}
	hours, _ := strconv.Atoi(offset[1:3])
	minutes, _ := strconv.Atoi(offset[4:6])
	secondsEastOfUTC := sign * (hours*60 + minutes) * 60
	location := time.FixedZone(offset, secondsEastOfUTC)
	return value.In(location).Format("2006-01-02T15:04:05-07:00")
}
