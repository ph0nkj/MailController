package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(server.Close)

	require.NoError(t, Run(context.Background(), server.Listener.Addr().String()))
}

func TestRunUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	err := Run(context.Background(), server.Listener.Addr().String())
	require.Error(t, err)
	require.Contains(t, err.Error(), "503")
}

func TestRunInvalidAddress(t *testing.T) {
	require.Error(t, Run(context.Background(), "not-an-address"))
}

func TestRunConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.Listener.Addr().String()
	server.Close()

	err := Run(context.Background(), addr)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "healthcheck:"))
}
