package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port         int
	AdminUser    string
	AdminPass    string
	AdminEnabled bool

	ProxyRefreshInterval time.Duration
	ProxyValidateTimeout time.Duration
	ProxyMaxConcurrent   int
	ProxyTestURL         string

	OpenCodeBaseURL string
	OpenCodeTimeout time.Duration
	OpenCodeModel   string

	RequestTimeout time.Duration
}

func Load() *Config {
	return &Config{
		Port:         envInt("PORT", 20120),
		AdminUser:    envStr("ADMIN_USER", "admin"),
		AdminPass:    envStr("ADMIN_PASS", "admin"),
		AdminEnabled: envBool("ADMIN_ENABLED", true),

		ProxyRefreshInterval: envDuration("PROXY_REFRESH_INTERVAL", 24*time.Hour),
		ProxyValidateTimeout: envDuration("PROXY_VALIDATE_TIMEOUT", 10*time.Second),
		ProxyMaxConcurrent:   envInt("PROXY_MAX_CONCURRENT", 50),
		ProxyTestURL:         envStr("PROXY_TEST_URL", "https://opencode.ai/zen/v1/models"),

		OpenCodeBaseURL: envStr("OPENCODE_BASE_URL", "https://opencode.ai"),
		OpenCodeTimeout: envDuration("OPENCODE_TIMEOUT", 120*time.Second),
		OpenCodeModel:   envStr("OPENCODE_DEFAULT_MODEL", "deepseek-v4-flash-free"),

		RequestTimeout: envDuration("REQUEST_TIMEOUT", 120*time.Second),
	}
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
