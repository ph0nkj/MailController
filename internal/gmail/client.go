package gmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

// Gmail label identifiers used by MailController.
const (
	LabelInbox  = "INBOX"
	LabelUnread = "UNREAD"
	LabelTrash  = "TRASH"
)

const (
	defaultPageSize = 25
	maxPageSize     = 100
	metadataWorkers = 10
	maxBodyBytes    = 1 << 20
	fullFormat      = "full"
	metadataFormat  = "metadata"
)

// ListOptions controls a paginated message listing.
type ListOptions struct {
	Query     string
	Label     string
	Unread    bool
	PageToken string
	PageSize  int
}

// ListResult is one page of messages.
type ListResult struct {
	Messages       []domain.Message
	NextPageToken  string
	EstimatedTotal int64
}

// Client is a Gmail API client bound to the connected account.
type Client struct {
	api   *gmailapi.Service
	email string
}

// Email returns the connected account email address.
func (c *Client) Email() string { return c.email }

// Client builds a Gmail API client for the connected account.
func (s *Service) Client(ctx context.Context) (*Client, error) {
	source, account, err := s.tokenSource(ctx)
	if err != nil {
		return nil, err
	}
	opts := []option.ClientOption{option.WithTokenSource(source)}
	if s.gmailEndpoint != "" {
		opts = append(opts, option.WithEndpoint(s.gmailEndpoint))
	}
	api, err := gmailapi.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gmail: create api client: %w", err)
	}
	return &Client{api: api, email: account.Email}, nil
}

// ListMessages lists messages using Gmail cursor pagination and fetches
// metadata for each message with a bounded worker pool.
func (c *Client) ListMessages(ctx context.Context, opts ListOptions) (ListResult, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	call := c.api.Users.Messages.List("me").
		MaxResults(int64(pageSize)).
		Context(ctx)
	if query := buildQuery(opts.Query, opts.Label, opts.Unread); query != "" {
		call = call.Q(query)
	}
	if opts.PageToken != "" {
		call = call.PageToken(opts.PageToken)
	}
	response, err := call.Do()
	if err != nil {
		return ListResult{}, fmt.Errorf("gmail: list messages: %w", err)
	}

	messages := make([]domain.Message, len(response.Messages))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(metadataWorkers)
	for i := range response.Messages {
		index := i
		group.Go(func() error {
			message, getErr := c.getMessage(groupCtx, response.Messages[index].Id, metadataFormat)
			if getErr != nil {
				return getErr
			}
			messages[index] = message
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return ListResult{}, err
	}
	return ListResult{
		Messages:       messages,
		NextPageToken:  response.NextPageToken,
		EstimatedTotal: response.ResultSizeEstimate,
	}, nil
}

// GetMessage returns a message with its plain-text and HTML bodies.
func (c *Client) GetMessage(ctx context.Context, id string) (domain.Message, error) {
	if strings.TrimSpace(id) == "" {
		return domain.Message{}, errors.New("gmail: message id is required")
	}
	return c.getMessage(ctx, id, fullFormat)
}

func (c *Client) getMessage(ctx context.Context, id, format string) (domain.Message, error) {
	raw, err := c.api.Users.Messages.Get("me", id).Format(format).Context(ctx).Do()
	if err != nil {
		return domain.Message{}, fmt.Errorf("gmail: get message %s: %w", id, err)
	}
	return convertMessage(raw), nil
}

func convertMessage(raw *gmailapi.Message) domain.Message {
	message := domain.Message{
		ID:       raw.Id,
		ThreadID: raw.ThreadId,
		Snippet:  raw.Snippet,
		Labels:   append([]string(nil), raw.LabelIds...),
		Unread:   contains(raw.LabelIds, LabelUnread),
	}
	if raw.Payload == nil {
		return message
	}
	for _, header := range raw.Payload.Headers {
		if header == nil {
			continue
		}
		switch strings.ToLower(header.Name) {
		case "from":
			message.From = header.Value
		case "to":
			message.To = header.Value
		case "cc":
			message.Cc = header.Value
		case "subject":
			message.Subject = header.Value
		case "date":
			if parsed, err := mail.ParseDate(header.Value); err == nil {
				message.Date = parsed
			}
		}
	}
	message.BodyText, message.BodyHTML = extractBodies(raw.Payload)
	return message
}

func extractBodies(part *gmailapi.MessagePart) (string, string) {
	var textBody, htmlBody string
	var walk func(*gmailapi.MessagePart)
	walk = func(current *gmailapi.MessagePart) {
		if current == nil {
			return
		}
		mimeType := strings.ToLower(current.MimeType)
		if current.Body != nil && current.Body.Data != "" {
			data := current.Body.Data
			switch {
			case mimeType == "text/plain" && textBody == "":
				textBody = decodeBody(data)
			case mimeType == "text/html" && htmlBody == "":
				htmlBody = decodeBody(data)
			}
		}
		for _, child := range current.Parts {
			walk(child)
		}
	}
	walk(part)
	return textBody, htmlBody
}

func decodeBody(data string) string {
	raw, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(data)
		if err != nil {
			return ""
		}
	}
	if len(raw) > maxBodyBytes {
		raw = raw[:maxBodyBytes]
	}
	return string(raw)
}

func buildQuery(query, label string, unread bool) string {
	parts := make([]string, 0, 3)
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(label); trimmed != "" {
		parts = append(parts, fmt.Sprintf("label:%q", trimmed))
	}
	if unread {
		parts = append(parts, "is:unread")
	}
	return strings.Join(parts, " ")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) tokenSource(ctx context.Context) (oauth2.TokenSource, domain.GoogleAccount, error) {
	cfg, configured, err := s.clientConfig(ctx)
	if err != nil {
		return nil, domain.GoogleAccount{}, err
	}
	if !configured {
		return nil, domain.GoogleAccount{}, ErrNotConfigured
	}
	account, err := s.accounts.Get(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, domain.GoogleAccount{}, ErrNotConnected
	case err != nil:
		return nil, domain.GoogleAccount{}, fmt.Errorf("gmail: load account: %w", err)
	}
	token, err := s.accountToken(account)
	if err != nil {
		return nil, domain.GoogleAccount{}, err
	}
	source := &persistingTokenSource{
		ctx:     ctx,
		base:    cfg.TokenSource(ctx, token),
		service: s,
		account: account,
		last:    *token,
	}
	return source, account, nil
}

type persistingTokenSource struct {
	ctx     context.Context
	base    oauth2.TokenSource
	service *Service
	account domain.GoogleAccount

	mu   sync.Mutex
	last oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	token, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if token.AccessToken == p.last.AccessToken &&
		token.RefreshToken == p.last.RefreshToken &&
		token.Expiry.Equal(p.last.Expiry) {
		return token, nil
	}
	enc, encErr := p.service.encryptToken(token)
	if encErr != nil {
		p.service.log.Warn("could not encrypt refreshed token", "error", encErr)
		return token, nil
	}
	p.account.TokenEnc = enc
	if saveErr := p.service.accounts.Save(context.WithoutCancel(p.ctx), p.account); saveErr != nil {
		p.service.log.Warn("could not persist refreshed token", "error", saveErr)
		return token, nil
	}
	p.last = *token
	return token, nil
}
