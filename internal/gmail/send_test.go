package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

func TestSendMessage(t *testing.T) {
	var captured gmailapi.Message
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/messages/send" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeJSON(t, w, map[string]string{"id": "sent-1"})
	}))

	id, err := client.SendMessage(context.Background(), "alice@example.com", "Hello",
		"Plain body", "<p>HTML body</p>")
	require.NoError(t, err)
	require.Equal(t, "sent-1", id)

	raw, err := base64.RawURLEncoding.DecodeString(captured.Raw)
	require.NoError(t, err)
	message := string(raw)
	require.Contains(t, message, "From: user@example.com\r\n")
	require.Contains(t, message, "To: alice@example.com\r\n")
	require.Contains(t, message, "Subject: Hello\r\n")
	require.Contains(t, message, "MIME-Version: 1.0\r\n")
	require.Contains(t, message, "Content-Type: multipart/alternative;")
	require.Contains(t, message, "Content-Type: text/plain; charset=\"UTF-8\"")
	require.Contains(t, message, "Content-Type: text/html; charset=\"UTF-8\"")

	boundary := mimeBoundary(t, message)
	parts := strings.Split(message, "--"+boundary)
	require.GreaterOrEqual(t, len(parts), 4)
	require.Equal(t, "Plain body", decodePart(t, parts[1]))
	require.Equal(t, "<p>HTML body</p>", decodePart(t, parts[2]))
}

func TestSendMessageTextOnlyAndUnicodeSubject(t *testing.T) {
	raw, err := buildMIMEMessage("user@example.com", "bob@example.org", "Привет, мир",
		"Plain only", "", time.Unix(0, 0))
	require.NoError(t, err)
	message := string(raw)
	require.Contains(t, message, "Content-Type: text/plain; charset=\"UTF-8\"")
	require.NotContains(t, message, "multipart/alternative")
	decodedSubject, err := new(mime.WordDecoder).DecodeHeader(
		message[strings.Index(message, "Subject: ")+len("Subject: ") : strings.Index(message, "\r\nDate: ")])
	require.NoError(t, err)
	require.Equal(t, "Привет, мир", decodedSubject)

	// The HTTP path still works for text-only messages.
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]string{"id": "sent-2"})
	}))
	id, err := client.SendMessage(context.Background(), "bob@example.org", "Hi", "Plain only", "")
	require.NoError(t, err)
	require.Equal(t, "sent-2", id)
}

func TestBuildMIMEMessageValidation(t *testing.T) {
	now := time.Unix(0, 0)
	tests := []struct {
		name    string
		from    string
		to      string
		subject string
		text    string
		html    string
	}{
		{name: "empty sender", to: "a@example.com", subject: "x", text: "body"},
		{name: "empty recipient", from: "u@example.com", subject: "x", text: "body"},
		{name: "display name", from: "u@example.com", to: "Alice <a@example.com>", subject: "x", text: "body"},
		{name: "header injection", from: "u@example.com", to: "a@example.com\r\nBcc: b@example.com", subject: "x", text: "body"},
		{name: "empty subject", from: "u@example.com", to: "a@example.com", text: "body"},
		{name: "long subject", from: "u@example.com", to: "a@example.com", subject: strings.Repeat("s", 501), text: "body"},
		{name: "empty body", from: "u@example.com", to: "a@example.com", subject: "x"},
		{name: "large body", from: "u@example.com", to: "a@example.com", subject: "x", text: strings.Repeat("b", maxSendBodyBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildMIMEMessage(tt.from, tt.to, tt.subject, tt.text, tt.html, now)
			require.ErrorIs(t, err, ErrInvalidMessage)
		})
	}

	// Header injection in the subject is neutralised, not rejected.
	raw, err := buildMIMEMessage("u@example.com", "a@example.com", "Hello\r\nBcc: evil@example.com", "body", "", now)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "\r\nBcc: evil@example.com")
}

func TestRetryable(t *testing.T) {
	client := &Client{}
	require.False(t, client.Retryable(nil))
	require.False(t, client.Retryable(ErrInvalidMessage))
	require.False(t, client.Retryable(context.Canceled))
	require.False(t, client.Retryable(context.DeadlineExceeded))
	require.False(t, client.Retryable(&googleapi.Error{Code: http.StatusBadRequest}))
	require.True(t, client.Retryable(&googleapi.Error{Code: http.StatusTooManyRequests}))
	require.True(t, client.Retryable(&googleapi.Error{Code: http.StatusInternalServerError}))
	require.True(t, client.Retryable(&googleapi.Error{
		Code:   http.StatusForbidden,
		Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}},
	}))
	require.False(t, client.Retryable(&googleapi.Error{Code: http.StatusForbidden}))
	require.True(t, client.Retryable(errors.New("network down")))
}

func TestBuildMIMEMessageEmptyHTMLFallbackText(t *testing.T) {
	raw, err := buildMIMEMessage("u@example.com", "a@example.com", "Subject", "", "<p>HTML</p>", time.Unix(0, 0))
	require.NoError(t, err)
	message := string(raw)
	boundary := mimeBoundary(t, message)
	parts := strings.Split(message, "--"+boundary)
	require.GreaterOrEqual(t, len(parts), 4)
	require.Contains(t, decodePart(t, parts[1]), "This message contains HTML content")
}

func mimeBoundary(t *testing.T, message string) string {
	t.Helper()
	const marker = `boundary="`
	start := strings.Index(message, marker)
	require.Greater(t, start, -1)
	start += len(marker)
	end := strings.Index(message[start:], `"`)
	require.Greater(t, end, -1)
	return message[start : start+end]
}

func decodePart(t *testing.T, part string) string {
	t.Helper()
	index := strings.Index(part, "\r\n\r\n")
	require.Greater(t, index, -1)
	encoded := strings.TrimSpace(part[index+4:])
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	return string(raw)
}

func TestSendMessageAPIError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"rate limited"}}`)
	}))
	_, err := client.SendMessage(context.Background(), "a@example.com", "Hi", "body", "")
	require.Error(t, err)
	require.True(t, client.Retryable(err))
}
