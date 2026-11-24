package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api, err := gmailapi.NewService(
		context.Background(),
		option.WithHTTPClient(server.Client()),
		option.WithEndpoint(server.URL+"/"),
	)
	require.NoError(t, err)
	return &Client{api: api, email: "user@example.com"}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fake response: %v", err)
	}
}

func messageJSON(id string, unread bool, body bool) map[string]any {
	labels := []string{"INBOX"}
	if unread {
		labels = append(labels, "UNREAD")
	}
	payload := map[string]any{
		"mimeType": "multipart/alternative",
		"headers": []map[string]string{
			{"name": "From", "value": "Alice <alice@example.com>"},
			{"name": "To", "value": "user@example.com"},
			{"name": "Subject", "value": "Hello " + id},
			{"name": "Date", "value": "Mon, 2 Jan 2006 15:04:05 -0700"},
		},
	}
	if body {
		payload["parts"] = []map[string]any{
			{
				"mimeType": "text/plain",
				"body": map[string]string{
					"data": base64.RawURLEncoding.EncodeToString([]byte("plain body")),
				},
			},
			{
				"mimeType": "text/html",
				"body": map[string]string{
					"data": base64.RawURLEncoding.EncodeToString([]byte("<p>html body</p>")),
				},
			},
		}
	}
	return map[string]any{
		"id":       id,
		"threadId": "thread-" + id,
		"snippet":  "snippet " + id,
		"labelIds": labels,
		"payload":  payload,
	}
}

func TestListMessages(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gmail/v1/users/me/messages":
			if got := r.URL.Query().Get("q"); got != `hello label:"INBOX" is:unread` {
				t.Errorf("unexpected query: %q", got)
			}
			writeJSON(t, w, map[string]any{
				"messages": []map[string]string{
					{"id": "m1", "threadId": "t1"},
					{"id": "m2", "threadId": "t2"},
				},
				"nextPageToken":      "next-page",
				"resultSizeEstimate": 42,
			})
		case strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")
			writeJSON(t, w, messageJSON(id, id == "m1", false))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	result, err := client.ListMessages(context.Background(), ListOptions{
		Query:    "hello",
		Label:    "INBOX",
		Unread:   true,
		PageSize: 1000,
	})
	require.NoError(t, err)
	require.Len(t, result.Messages, 2)
	require.Equal(t, "next-page", result.NextPageToken)
	require.Equal(t, int64(42), result.EstimatedTotal)
	require.Equal(t, "m1", result.Messages[0].ID)
	require.Equal(t, "thread-m2", result.Messages[1].ThreadID)
	require.Equal(t, "Alice <alice@example.com>", result.Messages[0].From)
	require.Equal(t, "Hello m1", result.Messages[0].Subject)
	require.Equal(t, 2006, result.Messages[0].Date.Year())
	require.True(t, result.Messages[0].Unread)
	require.False(t, result.Messages[1].Unread)
	require.Empty(t, result.Messages[0].BodyText)
}

func TestGetMessageFullBody(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "full" {
			t.Errorf("expected full format, got %q", r.URL.Query().Get("format"))
		}
		writeJSON(t, w, messageJSON("m1", true, true))
	}))

	message, err := client.GetMessage(context.Background(), "m1")
	require.NoError(t, err)
	require.Equal(t, "plain body", message.BodyText)
	require.Equal(t, "<p>html body</p>", message.BodyHTML)
	require.Equal(t, "user@example.com", message.To)
}

func TestGetMessageValidatesID(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	_, err := client.GetMessage(context.Background(), "  ")
	require.Error(t, err)
}

func TestBatchModifyChunks(t *testing.T) {
	var (
		mu      sync.Mutex
		sizes   []int
		added   []string
		removed []string
	)
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gmail/v1/users/me/messages/batchModify" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var request gmailapi.BatchModifyMessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		sizes = append(sizes, len(request.Ids))
		added = request.AddLabelIds
		removed = request.RemoveLabelIds
		mu.Unlock()
		writeJSON(t, w, map[string]string{})
	}))

	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	err := client.BatchModify(context.Background(), ids, []string{"STARRED"}, []string{"UNREAD"})
	require.NoError(t, err)
	require.Equal(t, []int{1000, 1}, sizes)
	require.Equal(t, []string{"STARRED"}, added)
	require.Equal(t, []string{"UNREAD"}, removed)
}

func TestBatchModifyValidation(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request expected")
	}))
	require.NoError(t, client.BatchModify(context.Background(), nil, []string{"X"}, nil))
	require.Error(t, client.BatchModify(context.Background(), []string{"1"}, nil, nil))
}

func TestCountMessagesAndListIDs(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "from:test" {
			t.Errorf("unexpected query %q", r.URL.Query().Get("q"))
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			writeJSON(t, w, map[string]any{
				"messages":      []map[string]string{{"id": "1"}, {"id": "2"}},
				"nextPageToken": "p2",
			})
		case "p2":
			writeJSON(t, w, map[string]any{
				"messages": []map[string]string{{"id": "3"}},
			})
		default:
			t.Errorf("unexpected page token %q", r.URL.Query().Get("pageToken"))
		}
	}))

	count, err := client.CountMessages(context.Background(), "from:test")
	require.NoError(t, err)
	require.Equal(t, int64(3), count)

	ids, err := client.ListMessageIDs(context.Background(), "from:test", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, ids)
}

func TestBuildQuery(t *testing.T) {
	require.Equal(t, "", buildQuery("", "", false))
	require.Equal(t, "hello", buildQuery(" hello ", "", false))
	require.Equal(t, `label:"My Label"`, buildQuery("", "My Label", false))
	require.Equal(t, "is:unread", buildQuery("", "", true))
	require.Equal(t, `hello label:"INBOX" is:unread`, buildQuery("hello", "INBOX", true))
}

func TestConvertMessageMissingPayload(t *testing.T) {
	message := convertMessage(&gmailapi.Message{Id: "x", LabelIds: []string{LabelUnread}})
	require.Equal(t, "x", message.ID)
	require.True(t, message.Unread)
	require.Empty(t, message.Subject)
	require.Empty(t, message.BodyText)
}

func TestDecodeBodyFallback(t *testing.T) {
	encoded := base64.URLEncoding.EncodeToString([]byte("padded"))
	require.Equal(t, "padded", decodeBody(encoded))
	require.Equal(t, "", decodeBody("!!!not-base64!!!"))
}

func TestListOptionsDefaults(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maxResults") != "25" {
			t.Errorf("unexpected maxResults %q", r.URL.Query().Get("maxResults"))
		}
		writeJSON(t, w, map[string]any{})
	}))
	result, err := client.ListMessages(context.Background(), ListOptions{})
	require.NoError(t, err)
	require.Empty(t, result.Messages)
}

func TestListMessageIDSCap(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"messages": []map[string]string{{"id": "1"}, {"id": "2"}},
		})
	}))
	ids, err := client.ListMessageIDs(context.Background(), "test", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, ids)
}

func TestClientEmail(t *testing.T) {
	client := &Client{email: "user@example.com"}
	require.Equal(t, "user@example.com", client.Email())
}
