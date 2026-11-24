package llm

import (
	"context"
	"fmt"
	"strings"
)

// Complete runs one guarded system+user completion with the configured
// provider. It is used by higher layers such as the mailbox agent planner.
func (s *Service) Complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if strings.TrimSpace(system) == "" || strings.TrimSpace(user) == "" {
		return "", fmt.Errorf("%w: prompt is empty", ErrInvalidRequest)
	}
	client, err := s.client(ctx)
	if err != nil {
		return "", err
	}
	answer, err := client.complete(ctx, system, user, maxTokens)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(answer) == "" {
		return "", fmt.Errorf("%w: empty response", ErrProvider)
	}
	return answer, nil
}
