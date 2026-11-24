// Package healthcheck probes the local HTTP health endpoint. It exists so the
// distroless container image, which has no shell or curl, can still run a
// Docker healthcheck using the service binary itself.
package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// maxBodyBytes bounds the response body read by the probe.
	maxBodyBytes = 512
	// requestTimeout bounds one probe.
	requestTimeout = 2 * time.Second
)

// Run requests /healthz from the service listening at addr and returns an
// error unless it answers 200 OK. An empty addr uses :8080.
func Run(ctx context.Context, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: parse address: %w", err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: build request: %w", err)
	}
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("healthcheck: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
