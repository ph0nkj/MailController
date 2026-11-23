package gmail

import (
	"context"
	"errors"
	"fmt"

	gmailapi "google.golang.org/api/gmail/v1"
)

const (
	// batchModifyLimit is the maximum number of message ids per batchModify
	// request supported by Gmail.
	batchModifyLimit = 1000
	// listPageLimit is the maximum page size for id-only listings.
	listPageLimit = 500
	// maxScanMessages bounds full-mailbox scans to protect API quota.
	maxScanMessages = 1_000_000
)

// BatchModify adds and removes labels for up to 1000 ids per request.
func (c *Client) BatchModify(ctx context.Context, ids, addLabels, removeLabels []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(addLabels) == 0 && len(removeLabels) == 0 {
		return errors.New("gmail: batch modify requires at least one label change")
	}
	for start := 0; start < len(ids); start += batchModifyLimit {
		end := start + batchModifyLimit
		if end > len(ids) {
			end = len(ids)
		}
		request := &gmailapi.BatchModifyMessagesRequest{
			Ids:            ids[start:end],
			AddLabelIds:    addLabels,
			RemoveLabelIds: removeLabels,
		}
		if err := c.api.Users.Messages.BatchModify("me", request).Context(ctx).Do(); err != nil {
			return fmt.Errorf("gmail: batch modify %d messages: %w", end-start, err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("gmail: batch modify: %w", err)
		}
	}
	return nil
}

// CountMessages returns the number of messages matching a Gmail query. It
// counts exact ids up to a one-million-message safety cap.
func (c *Client) CountMessages(ctx context.Context, query string) (int64, error) {
	var (
		count int64
		token string
	)
	for {
		response, err := c.listPage(ctx, query, token, listPageLimit)
		if err != nil {
			return 0, err
		}
		count += int64(len(response.Messages))
		if response.NextPageToken == "" || count >= maxScanMessages {
			return count, nil
		}
		token = response.NextPageToken
	}
}

// ListMessageIDs returns ids matching a Gmail query, up to limit. A limit of
// zero or less uses the safety cap.
func (c *Client) ListMessageIDs(ctx context.Context, query string, limit int) ([]string, error) {
	if limit <= 0 || limit > maxScanMessages {
		limit = maxScanMessages
	}
	ids := make([]string, 0, min(limit, 256))
	token := ""
	for len(ids) < limit {
		pageSize := listPageLimit
		if remaining := limit - len(ids); remaining < pageSize {
			pageSize = remaining
		}
		response, err := c.listPage(ctx, query, token, int64(pageSize))
		if err != nil {
			return nil, err
		}
		for _, message := range response.Messages {
			if message.Id != "" {
				ids = append(ids, message.Id)
			}
		}
		if response.NextPageToken == "" || len(response.Messages) == 0 {
			break
		}
		token = response.NextPageToken
	}
	return ids, nil
}

func (c *Client) listPage(ctx context.Context, query, pageToken string, pageSize int64) (*gmailapi.ListMessagesResponse, error) {
	call := c.api.Users.Messages.List("me").
		MaxResults(pageSize).
		Context(ctx)
	if query != "" {
		call = call.Q(query)
	}
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	response, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: list message ids: %w", err)
	}
	return response, nil
}
