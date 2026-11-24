package gmail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"

	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

const (
	// maxSendSubjectLength keeps generated headers bounded.
	maxSendSubjectLength = 500
	// maxSendBodyBytes caps each body part.
	maxSendBodyBytes = 1 << 20
)

// ErrInvalidMessage is returned when an outgoing message fails validation.
var ErrInvalidMessage = errors.New("gmail: invalid message")

// SendMessage sends a multipart message through the connected account and
// returns the Gmail message id.
func (c *Client) SendMessage(ctx context.Context, to, subject, textBody, htmlBody string) (string, error) {
	raw, err := buildMIMEMessage(c.email, to, subject, textBody, htmlBody, time.Now())
	if err != nil {
		return "", err
	}
	message := &gmailapi.Message{Raw: base64.RawURLEncoding.EncodeToString(raw)}
	sent, err := c.api.Users.Messages.Send("me", message).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("gmail: send message: %w", err)
	}
	return sent.Id, nil
}

// Retryable reports whether an error from SendMessage is likely transient and
// worth retrying.
func (c *Client) Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrInvalidMessage) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == 429:
			return true
		case apiErr.Code >= 500:
			return true
		case apiErr.Code == 403:
			for _, item := range apiErr.Errors {
				if strings.Contains(strings.ToLower(item.Reason), "rate") {
					return true
				}
			}
			return strings.Contains(strings.ToLower(apiErr.Message), "rate limit")
		default:
			return false
		}
	}
	return true
}

func buildMIMEMessage(from, to, subject, textBody, htmlBody string, now time.Time) ([]byte, error) {
	if strings.ContainsAny(to, "\r\n") {
		return nil, fmt.Errorf("%w: recipient contains a line break", ErrInvalidMessage)
	}
	from = sanitizeHeader(from)
	to = strings.TrimSpace(to)
	subject = sanitizeHeader(subject)
	if from == "" {
		return nil, fmt.Errorf("%w: sender is empty", ErrInvalidMessage)
	}
	if to == "" {
		return nil, fmt.Errorf("%w: recipient is empty", ErrInvalidMessage)
	}
	parsedTo, err := mail.ParseAddress(to)
	if err != nil || parsedTo.Name != "" || !strings.EqualFold(parsedTo.Address, to) {
		return nil, fmt.Errorf("%w: recipient is not a plain address", ErrInvalidMessage)
	}
	if subject == "" {
		return nil, fmt.Errorf("%w: subject is empty", ErrInvalidMessage)
	}
	if len(subject) > maxSendSubjectLength {
		return nil, fmt.Errorf("%w: subject is too long", ErrInvalidMessage)
	}
	if len(textBody) > maxSendBodyBytes || len(htmlBody) > maxSendBodyBytes {
		return nil, fmt.Errorf("%w: body is too large", ErrInvalidMessage)
	}
	if strings.TrimSpace(textBody) == "" && strings.TrimSpace(htmlBody) == "" {
		return nil, fmt.Errorf("%w: body is empty", ErrInvalidMessage)
	}

	var builder strings.Builder
	builder.WriteString("From: ")
	builder.WriteString(from)
	builder.WriteString("\r\nTo: ")
	builder.WriteString(to)
	builder.WriteString("\r\nSubject: ")
	builder.WriteString(mime.QEncoding.Encode("UTF-8", subject))
	builder.WriteString("\r\nDate: ")
	builder.WriteString(now.Format(time.RFC1123Z))
	builder.WriteString("\r\nMIME-Version: 1.0\r\n")

	if strings.TrimSpace(htmlBody) == "" {
		builder.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		builder.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		builder.WriteString(base64Body(textBody))
		return []byte(builder.String()), nil
	}

	boundary, err := newMIMEBoundary()
	if err != nil {
		return nil, err
	}
	builder.WriteString("Content-Type: multipart/alternative; boundary=\"")
	builder.WriteString(boundary)
	builder.WriteString("\"\r\n\r\n")

	builder.WriteString("--")
	builder.WriteString(boundary)
	builder.WriteString("\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n")
	builder.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	if strings.TrimSpace(textBody) == "" {
		textBody = "This message contains HTML content. Please use an HTML-capable mail client."
	}
	builder.WriteString(base64Body(textBody))

	builder.WriteString("--")
	builder.WriteString(boundary)
	builder.WriteString("\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n")
	builder.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	builder.WriteString(base64Body(htmlBody))
	builder.WriteString("--")
	builder.WriteString(boundary)
	builder.WriteString("--\r\n")
	return []byte(builder.String()), nil
}

func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func base64Body(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var builder strings.Builder
	for len(encoded) > 76 {
		builder.WriteString(encoded[:76])
		builder.WriteString("\r\n")
		encoded = encoded[76:]
	}
	builder.WriteString(encoded)
	builder.WriteString("\r\n")
	return builder.String()
}

func newMIMEBoundary() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gmail: generate mime boundary: %w", err)
	}
	return "mc_" + hex.EncodeToString(buf), nil
}
