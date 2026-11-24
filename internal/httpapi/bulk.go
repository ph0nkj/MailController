package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// Bulk action names accepted by the bulk endpoint.
const (
	bulkMarkRead   = "mark_read"
	bulkMarkUnread = "mark_unread"
	bulkArchive    = "archive"
	bulkTrash      = "trash"
)

const (
	// bulkMaxMessages caps one bulk request.
	bulkMaxMessages = 1000
	// bulkBodyLimit caps the request body size.
	bulkBodyLimit = 256 << 10
)

type bulkRequest struct {
	IDs     []string `json:"ids"`
	Action  string   `json:"action"`
	Confirm bool     `json:"confirm"`
}

type bulkResponse struct {
	Action   string `json:"action"`
	Affected int    `json:"affected"`
}

func bulkLabels(action string) (addLabels, removeLabels []string, err error) {
	switch action {
	case bulkMarkRead:
		return nil, []string{"UNREAD"}, nil
	case bulkMarkUnread:
		return []string{"UNREAD"}, nil, nil
	case bulkArchive:
		return nil, []string{"INBOX"}, nil
	case bulkTrash:
		return []string{"TRASH"}, nil, nil
	default:
		return nil, nil, errors.New("action must be one of mark_read, mark_unread, archive, trash")
	}
}

func normalizeIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("ids is required")
	}
	if len(ids) > bulkMaxMessages {
		return nil, errors.New("too many ids; select at most 1000 messages")
	}
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 256 {
			return nil, errors.New("message ids must be non-empty and at most 256 characters")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique, nil
}

func (a *API) handleMailBulk(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, bulkBodyLimit)
	var req bulkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	addLabels, removeLabels, err := bulkLabels(req.Action)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ids, err := normalizeIDs(req.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Action == bulkTrash && !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirmation_required", "trash requires confirm: true")
		return
	}
	if err := a.mailbox.BatchModify(r.Context(), ids, addLabels, removeLabels); err != nil {
		a.writeMailError(w, "bulk mailbox action", err)
		return
	}
	a.mailCache.Clear()
	a.auditAction(r.Context(), domain.AuditEntry{
		Action:  "mail.bulk",
		Details: auditDetails(map[string]any{"action": req.Action, "count": len(ids)}),
	})
	writeJSON(w, http.StatusOK, bulkResponse{Action: req.Action, Affected: len(ids)})
}
