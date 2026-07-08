package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr          string
	APIKey              string
	BaseURL             string
	StatePath           string
	InitialINN          string
	InitialRefreshToken string
	InitialDeviceID     string
	UserAgent           string
	HTTPTimeout         time.Duration
	TimezoneOffset      string
	DefaultServiceName  string
	PaymentType         string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddr:          env("VFF_FISCAL_LISTEN_ADDR", ":8080"),
		APIKey:              strings.TrimSpace(os.Getenv("VFF_FISCAL_API_KEY")),
		BaseURL:             strings.TrimRight(env("LKNPD_BASE_URL", "https://lknpd.nalog.ru/api/v1"), "/"),
		StatePath:           env("VFF_FISCAL_STATE_PATH", "/var/lib/vff-fiscal/state.json"),
		InitialINN:          strings.TrimSpace(os.Getenv("LKNPD_INN")),
		InitialRefreshToken: strings.TrimSpace(os.Getenv("LKNPD_REFRESH_TOKEN")),
		InitialDeviceID:     strings.TrimSpace(os.Getenv("LKNPD_DEVICE_ID")),
		UserAgent:           env("LKNPD_USER_AGENT", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36"),
		TimezoneOffset:      env("LKNPD_TIMEZONE_OFFSET", "+03:00"),
		DefaultServiceName:  env("VFF_FISCAL_SERVICE_NAME", "Услуга доступа к VPN-сервису VPN for Friends"),
		PaymentType:         strings.ToUpper(env("LKNPD_PAYMENT_TYPE", "WIRE")),
	}

	timeoutSeconds, err := strconv.Atoi(env("LKNPD_HTTP_TIMEOUT_SECONDS", "30"))
	if err != nil || timeoutSeconds < 1 || timeoutSeconds > 120 {
		return Config{}, errors.New("LKNPD_HTTP_TIMEOUT_SECONDS must be an integer from 1 to 120")
	}
	cfg.HTTPTimeout = time.Duration(timeoutSeconds) * time.Second

	if cfg.APIKey == "" {
		return Config{}, errors.New("VFF_FISCAL_API_KEY is required")
	}
	if cfg.PaymentType != "WIRE" && cfg.PaymentType != "CASH" {
		return Config{}, errors.New("LKNPD_PAYMENT_TYPE must be WIRE or CASH")
	}
	if !validOffset(cfg.TimezoneOffset) {
		return Config{}, errors.New("LKNPD_TIMEZONE_OFFSET must look like +03:00")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func validOffset(value string) bool {
	if len(value) != 6 || (value[0] != '+' && value[0] != '-') || value[3] != ':' {
		return false
	}
	for _, i := range []int{1, 2, 4, 5} {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}

	hours, errHours := strconv.Atoi(value[1:3])
	minutes, errMinutes := strconv.Atoi(value[4:6])
	if errHours != nil || errMinutes != nil || minutes > 59 || hours > 14 {
		return false
	}
	return hours < 14 || minutes == 0
}
