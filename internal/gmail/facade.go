package gmail

import (
	"context"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// The methods below expose the connected mailbox as a stable facade for HTTP
// handlers and other consumers, hiding the underlying Gmail client.

// ListMessages lists a page of messages.
func (s *Service) ListMessages(ctx context.Context, opts ListOptions) (ListResult, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return ListResult{}, err
	}
	return client.ListMessages(ctx, opts)
}

// GetMessage returns one message with bodies.
func (s *Service) GetMessage(ctx context.Context, id string) (domain.Message, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return domain.Message{}, err
	}
	return client.GetMessage(ctx, id)
}

// BatchModify applies label changes to messages.
func (s *Service) BatchModify(ctx context.Context, ids, addLabels, removeLabels []string) error {
	client, err := s.Client(ctx)
	if err != nil {
		return err
	}
	return client.BatchModify(ctx, ids, addLabels, removeLabels)
}

// CountMessages counts messages matching a Gmail query.
func (s *Service) CountMessages(ctx context.Context, query string) (int64, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return 0, err
	}
	return client.CountMessages(ctx, query)
}

// ListMessageIDs returns message ids matching a Gmail query.
func (s *Service) ListMessageIDs(ctx context.Context, query string, limit int) ([]string, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return nil, err
	}
	return client.ListMessageIDs(ctx, query, limit)
}
