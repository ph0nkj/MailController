package config

import (
	"encoding/base64"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func validKey() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MC_SECRET_KEY", validKey())
	t.Setenv("MC_ADMIN_PASSWORD", "supersecret")
	t.Setenv("MC_ADDR", ":9090")
	t.Setenv("MC_DB_PATH", "/tmp/mc-test.db")
	t.Setenv("MC_BASE_URL", "https://mail.example.com/")
	t.Setenv("MC_SEND_RATE_PER_MIN", "120")
	t.Setenv("MC_DAILY_SEND_LIMIT", "1000")
	t.Setenv("MC_LLM_MAX_MESSAGES", "50")
	t.Setenv("MC_LOG_LEVEL", "debug")
}

func TestLoadValid(t *testing.T) {
	setValidEnv(t)
	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, ":9090", cfg.Addr)
	require.Equal(t, "/tmp/mc-test.db", cfg.DBPath)
	require.Equal(t, "https://mail.example.com", cfg.BaseURL)
	require.Equal(t, 120, cfg.SendRatePerMin)
	require.Equal(t, 1000, cfg.DailySendLimit)
	require.Equal(t, 50, cfg.LLMMaxMessages)
	require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	require.Equal(t, 32, len(cfg.SecretKey))
	require.True(t, cfg.CookieSecure)
	require.True(t, cfg.UnsubscribeEnabled)
}

func TestLoadDefaults(t *testing.T) {
	setValidEnv(t)
	t.Setenv("MC_ADDR", "")
	t.Setenv("MC_DB_PATH", "")
	t.Setenv("MC_BASE_URL", "")
	t.Setenv("MC_SEND_RATE_PER_MIN", "")
	t.Setenv("MC_DAILY_SEND_LIMIT", "")
	t.Setenv("MC_LLM_MAX_MESSAGES", "")
	t.Setenv("MC_LOG_LEVEL", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, DefaultAddr, cfg.Addr)
	require.Equal(t, DefaultDBPath, cfg.DBPath)
	require.Equal(t, DefaultBaseURL, cfg.BaseURL)
	require.Equal(t, DefaultSendRatePerMin, cfg.SendRatePerMin)
	require.Equal(t, DefaultDailySendLimit, cfg.DailySendLimit)
	require.Equal(t, DefaultLLMMaxMessages, cfg.LLMMaxMessages)
	require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	require.False(t, cfg.CookieSecure)
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing secret", key: "MC_SECRET_KEY", value: ""},
		{name: "bad secret base64", key: "MC_SECRET_KEY", value: "!!!notbase64!!!"},
		{name: "short secret", key: "MC_SECRET_KEY", value: base64.StdEncoding.EncodeToString([]byte("short"))},
		{name: "short password", key: "MC_ADMIN_PASSWORD", value: "short"},
		{name: "bad base url", key: "MC_BASE_URL", value: "not a url"},
		{name: "bad rate", key: "MC_SEND_RATE_PER_MIN", value: "zero"},
		{name: "negative rate", key: "MC_SEND_RATE_PER_MIN", value: "-5"},
		{name: "bad limit", key: "MC_DAILY_SEND_LIMIT", value: "nope"},
		{name: "zero limit", key: "MC_DAILY_SEND_LIMIT", value: "0"},
		{name: "bad llm max", key: "MC_LLM_MAX_MESSAGES", value: "huge"},
		{name: "bad agent max steps", key: "MC_AGENT_MAX_STEPS", value: "21"},
		{name: "bad log level", key: "MC_LOG_LEVEL", value: "verbose"},
		{name: "bad unsubscribe email", key: "MC_UNSUBSCRIBE_EMAIL", value: "not an email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(tt.key, tt.value)
			_, err := Load()
			require.Error(t, err)
		})
	}
}

func TestValidate(t *testing.T) {
	cfg := Config{
		Addr:             ":8080",
		DBPath:           "/tmp/x.db",
		SecretKey:        make([]byte, 32),
		AdminPassword:    "password1",
		BaseURL:          "http://localhost:8080",
		SendRatePerMin:   60,
		DailySendLimit:   500,
		LLMMaxMessages:   200,
		AgentMaxMessages: 10000,
		AgentMaxSteps:    5,
		LLMBodyChars:     2000,
	}
	require.NoError(t, cfg.Validate())

	cfg.SecretKey = []byte("short")
	require.Error(t, cfg.Validate())
}

func TestParseLogLevel(t *testing.T) {
	for raw, want := range map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	} {
		got, err := parseLogLevel(raw)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := parseLogLevel("trace")
	require.Error(t, err)
}
