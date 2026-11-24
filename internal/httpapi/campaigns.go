package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/mailer"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

const (
	// campaignBodyLimit caps JSON campaign payloads.
	campaignBodyLimit = 1 << 20
	// campaignImportBodyLimit caps pasted or uploaded recipient lists.
	campaignImportBodyLimit = 16 << 20
	// campaignMaxBodies caps each message body.
	campaignMaxBodyBytes = 200 << 10
	// campaignMaxSubject caps the subject line.
	campaignMaxSubject = 300
	// campaignMaxName caps the campaign name.
	campaignMaxName = 120
	// campaignMaxDailyLimit and campaignMaxRatePerMin bound delivery settings.
	campaignMaxDailyLimit  = 10_000
	campaignMaxRatePerMin  = 600
	campaignDefaultDaily   = 500
	campaignDefaultRate    = 60
	campaignRecipientPage  = 50
	campaignRecipientMax   = 200
	campaignRecipientPages = 100_000
)

// CampaignStore is the persistence surface required by campaign handlers.
type CampaignStore interface {
	Create(ctx context.Context, campaign *domain.Campaign) error
	Get(ctx context.Context, id string) (domain.Campaign, error)
	List(ctx context.Context) ([]domain.Campaign, error)
	Update(ctx context.Context, campaign domain.Campaign) error
	DeleteDraft(ctx context.Context, id string) error
	AddRecipients(ctx context.Context, campaignID string, emails []string) (int, error)
	ListRecipients(ctx context.Context, campaignID, status string, limit, offset int) ([]domain.Recipient, int, error)
	Stats(ctx context.Context, campaignID string) (domain.RecipientStats, error)
}

// CampaignRunner starts and pauses campaign delivery jobs.
type CampaignRunner interface {
	Start(ctx context.Context, campaignID string) (string, error)
	Pause(ctx context.Context, campaignID string) error
}

type recipientStatsDTO struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type campaignDTO struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Subject    string            `json:"subject"`
	Status     string            `json:"status"`
	DailyLimit int               `json:"daily_limit"`
	RatePerMin int               `json:"rate_per_min"`
	CreatedAt  time.Time         `json:"created_at"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Stats      recipientStatsDTO `json:"stats"`
}

type campaignDetailDTO struct {
	campaignDTO
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
}

type campaignListDTO struct {
	Items []campaignDTO `json:"items"`
	Total int           `json:"total"`
}

type recipientDTO struct {
	ID     int64      `json:"id"`
	Email  string     `json:"email"`
	Status string     `json:"status"`
	Error  string     `json:"error,omitempty"`
	SentAt *time.Time `json:"sent_at,omitempty"`
}

type recipientListDTO struct {
	Items    []recipientDTO `json:"items"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

type campaignRequest struct {
	Name       string `json:"name"`
	Subject    string `json:"subject"`
	BodyText   string `json:"body_text"`
	BodyHTML   string `json:"body_html"`
	DailyLimit int    `json:"daily_limit"`
	RatePerMin int    `json:"rate_per_min"`
}

type campaignStartRequest struct {
	Confirm bool `json:"confirm"`
}

type campaignStartResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

type campaignStatusResponse struct {
	Status string `json:"status"`
}

type campaignImportResponse struct {
	Imported       int      `json:"imported"`
	Duplicates     int      `json:"duplicates"`
	Invalid        int      `json:"invalid"`
	InvalidSamples []string `json:"invalid_samples"`
	Total          int      `json:"total"`
}

func toCampaignDTO(campaign domain.Campaign, stats domain.RecipientStats) campaignDTO {
	return campaignDTO{
		ID:         campaign.ID,
		Name:       campaign.Name,
		Subject:    campaign.Subject,
		Status:     campaign.Status,
		DailyLimit: campaign.DailyLimit,
		RatePerMin: campaign.RatePerMin,
		CreatedAt:  campaign.CreatedAt,
		StartedAt:  timePointer(campaign.StartedAt),
		FinishedAt: timePointer(campaign.FinishedAt),
		Stats: recipientStatsDTO{
			Total:   stats.Total,
			Pending: stats.Pending,
			Sent:    stats.Sent,
			Failed:  stats.Failed,
			Skipped: stats.Skipped,
		},
	}
}

func toCampaignDetailDTO(campaign domain.Campaign, stats domain.RecipientStats) campaignDetailDTO {
	return campaignDetailDTO{
		campaignDTO: toCampaignDTO(campaign, stats),
		BodyText:    campaign.BodyText,
		BodyHTML:    campaign.BodyHTML,
	}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func decodeCampaignBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, campaignBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

func (req campaignRequest) apply(base domain.Campaign) (domain.Campaign, error) {
	base.Name = strings.TrimSpace(req.Name)
	if base.Name == "" || len(base.Name) > campaignMaxName {
		return domain.Campaign{}, fmt.Errorf("name must be 1 to %d characters", campaignMaxName)
	}
	base.Subject = strings.TrimSpace(req.Subject)
	if base.Subject == "" || len(base.Subject) > campaignMaxSubject {
		return domain.Campaign{}, fmt.Errorf("subject must be 1 to %d characters", campaignMaxSubject)
	}
	if strings.ContainsAny(base.Subject, "\r\n") {
		return domain.Campaign{}, errors.New("subject must be a single line")
	}
	if len(req.BodyText) > campaignMaxBodyBytes || len(req.BodyHTML) > campaignMaxBodyBytes {
		return domain.Campaign{}, errors.New("each body must be at most 200 KiB")
	}
	if strings.TrimSpace(req.BodyText) == "" && strings.TrimSpace(req.BodyHTML) == "" {
		return domain.Campaign{}, errors.New("a text or HTML body is required")
	}
	base.BodyText = req.BodyText
	base.BodyHTML = req.BodyHTML
	base.DailyLimit = req.DailyLimit
	if base.DailyLimit == 0 {
		base.DailyLimit = campaignDefaultDaily
	}
	if base.DailyLimit < 1 || base.DailyLimit > campaignMaxDailyLimit {
		return domain.Campaign{}, fmt.Errorf("daily_limit must be between 1 and %d", campaignMaxDailyLimit)
	}
	base.RatePerMin = req.RatePerMin
	if base.RatePerMin == 0 {
		base.RatePerMin = campaignDefaultRate
	}
	if base.RatePerMin < 1 || base.RatePerMin > campaignMaxRatePerMin {
		return domain.Campaign{}, fmt.Errorf("rate_per_min must be between 1 and %d", campaignMaxRatePerMin)
	}
	return base, nil
}

func (a *API) handleCampaignList(w http.ResponseWriter, r *http.Request) {
	campaigns, err := a.campaigns.List(r.Context())
	if err != nil {
		a.writeCampaignError(w, "list campaigns", err)
		return
	}
	items := make([]campaignDTO, 0, len(campaigns))
	for _, campaign := range campaigns {
		stats, statsErr := a.campaigns.Stats(r.Context(), campaign.ID)
		if statsErr != nil {
			a.writeCampaignError(w, "load campaign stats", statsErr)
			return
		}
		items = append(items, toCampaignDTO(campaign, stats))
	}
	writeJSON(w, http.StatusOK, campaignListDTO{Items: items, Total: len(items)})
}

func (a *API) handleCampaignCreate(w http.ResponseWriter, r *http.Request) {
	var req campaignRequest
	if !decodeCampaignBody(w, r, &req) {
		return
	}
	campaign, err := req.apply(domain.Campaign{Status: domain.CampaignDraft})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := a.campaigns.Create(r.Context(), &campaign); err != nil {
		a.writeCampaignError(w, "create campaign", err)
		return
	}
	a.auditAction(r.Context(), domain.AuditEntry{
		Action:  "campaign.create",
		Details: auditDetails(map[string]any{"campaign_id": campaign.ID}),
	})
	stats, err := a.campaigns.Stats(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	writeJSON(w, http.StatusCreated, toCampaignDetailDTO(campaign, stats))
}

func (a *API) handleCampaignGet(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	stats, err := a.campaigns.Stats(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	writeJSON(w, http.StatusOK, toCampaignDetailDTO(campaign, stats))
}

func (a *API) handleCampaignUpdate(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	if campaign.Status != domain.CampaignDraft {
		writeError(w, http.StatusConflict, "campaign_not_draft", "only draft campaigns can be edited")
		return
	}
	var req campaignRequest
	if !decodeCampaignBody(w, r, &req) {
		return
	}
	updated, err := req.apply(campaign)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := a.campaigns.Update(r.Context(), updated); err != nil {
		a.writeCampaignError(w, "update campaign", err)
		return
	}
	a.auditAction(r.Context(), domain.AuditEntry{
		Action:  "campaign.update",
		Details: auditDetails(map[string]any{"campaign_id": updated.ID}),
	})
	stats, err := a.campaigns.Stats(r.Context(), updated.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	writeJSON(w, http.StatusOK, toCampaignDetailDTO(updated, stats))
}

func (a *API) handleCampaignDelete(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	if campaign.Status != domain.CampaignDraft {
		writeError(w, http.StatusConflict, "campaign_not_draft", "only draft campaigns can be deleted")
		return
	}
	if err := a.campaigns.DeleteDraft(r.Context(), campaign.ID); err != nil {
		a.writeCampaignError(w, "delete campaign", err)
		return
	}
	a.auditAction(r.Context(), domain.AuditEntry{
		Action:  "campaign.delete",
		Details: auditDetails(map[string]any{"campaign_id": campaign.ID}),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCampaignRecipientsList(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validRecipientStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown recipient status")
		return
	}
	page, pageSize, err := parseRecipientPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	recipients, total, err := a.campaigns.ListRecipients(
		r.Context(), campaign.ID, status, pageSize, (page-1)*pageSize)
	if err != nil {
		a.writeCampaignError(w, "list recipients", err)
		return
	}
	items := make([]recipientDTO, len(recipients))
	for i, recipient := range recipients {
		items[i] = recipientDTO{
			ID:     recipient.ID,
			Email:  recipient.Email,
			Status: recipient.Status,
			Error:  recipient.Error,
			SentAt: timePointer(recipient.SentAt),
		}
	}
	writeJSON(w, http.StatusOK, recipientListDTO{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

func (a *API) handleCampaignRecipientsImport(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	if campaign.Status != domain.CampaignDraft {
		writeError(w, http.StatusConflict, "campaign_not_draft", "recipients can only be imported into drafts")
		return
	}
	stats, err := a.campaigns.Stats(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	remaining := mailer.DefaultMaxRecipients - stats.Total
	if remaining <= 0 {
		writeError(w, http.StatusBadRequest, "recipient_limit", "campaign already has the maximum number of recipients")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, campaignImportBodyLimit)
	emails := make([]string, 0, min(remaining, 1024))
	parseStats, err := mailer.ParseRecipients(r.Body, mailer.ParseOptions{MaxRecipients: remaining},
		func(email string) error {
			emails = append(emails, email)
			return nil
		})
	if err != nil {
		if errors.Is(err, mailer.ErrTooManyRecipients) {
			writeError(w, http.StatusBadRequest, "recipient_limit", err.Error())
			return
		}
		a.log.Warn("parse recipient list", "campaign_id", campaign.ID, "error", err)
		writeError(w, http.StatusBadRequest, "invalid_recipients", "could not parse the recipient list")
		return
	}
	if len(emails) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_recipients", "no valid email addresses found")
		return
	}
	imported, err := a.campaigns.AddRecipients(r.Context(), campaign.ID, emails)
	if err != nil {
		a.writeCampaignError(w, "import recipients", err)
		return
	}
	duplicates := parseStats.Duplicates + (len(emails) - imported)
	a.auditAction(r.Context(), domain.AuditEntry{
		Action: "campaign.import",
		Details: auditDetails(map[string]any{
			"campaign_id": campaign.ID,
			"imported":    imported,
			"duplicates":  duplicates,
			"invalid":     parseStats.Invalid,
		}),
	})
	finalStats, err := a.campaigns.Stats(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	writeJSON(w, http.StatusOK, campaignImportResponse{
		Imported:       imported,
		Duplicates:     duplicates,
		Invalid:        parseStats.Invalid,
		InvalidSamples: parseStats.InvalidSamples,
		Total:          finalStats.Total,
	})
}

func (a *API) handleCampaignStart(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	var req campaignStartRequest
	if !decodeCampaignBody(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirmation_required", "sending a campaign requires confirm: true")
		return
	}
	if campaign.Status != domain.CampaignDraft && campaign.Status != domain.CampaignPaused {
		writeError(w, http.StatusConflict, "invalid_state", "campaign is not ready to start")
		return
	}
	stats, err := a.campaigns.Stats(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignError(w, "load campaign stats", err)
		return
	}
	if stats.Pending == 0 {
		writeError(w, http.StatusConflict, "campaign_empty", "campaign has no pending recipients")
		return
	}
	jobID, err := a.campaignRunner.Start(r.Context(), campaign.ID)
	if err != nil {
		a.writeCampaignStartError(w, err)
		return
	}
	a.auditAction(r.Context(), domain.AuditEntry{
		Action: "campaign.start",
		Details: auditDetails(map[string]any{
			"campaign_id": campaign.ID,
			"pending":     stats.Pending,
			"daily_limit": campaign.DailyLimit,
			"rate":        campaign.RatePerMin,
		}),
	})
	writeJSON(w, http.StatusAccepted, campaignStartResponse{JobID: jobID, Status: domain.CampaignRunning})
}

func (a *API) handleCampaignPause(w http.ResponseWriter, r *http.Request) {
	campaign, ok := a.loadCampaign(w, r)
	if !ok {
		return
	}
	switch campaign.Status {
	case domain.CampaignRunning:
		pauseCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer cancel()
		if err := a.campaignRunner.Pause(pauseCtx, campaign.ID); err != nil {
			a.writeCampaignStartError(w, err)
			return
		}
		current, err := a.campaigns.Get(r.Context(), campaign.ID)
		if err != nil {
			a.writeCampaignError(w, "reload campaign", err)
			return
		}
		writeJSON(w, http.StatusOK, campaignStatusResponse{Status: current.Status})
	case domain.CampaignPaused:
		writeJSON(w, http.StatusOK, campaignStatusResponse{Status: campaign.Status})
	default:
		writeError(w, http.StatusConflict, "invalid_state", "campaign is not running")
	}
}

func (a *API) loadCampaign(w http.ResponseWriter, r *http.Request) (domain.Campaign, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "campaign id is required")
		return domain.Campaign{}, false
	}
	campaign, err := a.campaigns.Get(r.Context(), id)
	if err != nil {
		a.writeCampaignError(w, "load campaign", err)
		return domain.Campaign{}, false
	}
	return campaign, true
}

func (a *API) writeCampaignError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "campaign_not_found", "campaign not found")
	case errors.Is(err, store.ErrNotDraft):
		writeError(w, http.StatusConflict, "campaign_not_draft", "campaign is not a draft")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	default:
		a.log.Error(operation, "error", err)
		writeError(w, http.StatusInternalServerError, "campaign_error", "campaign operation failed")
	}
}

func (a *API) writeCampaignStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "campaign_not_found", "campaign not found")
	case errors.Is(err, mailer.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, "campaign_busy", "campaign is already running")
	case errors.Is(err, mailer.ErrNoRecipients):
		writeError(w, http.StatusConflict, "campaign_empty", "campaign has no pending recipients")
	case errors.Is(err, mailer.ErrInvalidCampaignState):
		writeError(w, http.StatusConflict, "invalid_state", "campaign is not ready to start")
	case errors.Is(err, gmail.ErrNotConnected):
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
	case errors.Is(err, gmail.ErrNotConfigured):
		writeError(w, http.StatusConflict, "google_not_configured", "save Google OAuth client credentials first")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	default:
		a.log.Error("campaign delivery", "error", err)
		writeError(w, http.StatusBadGateway, "campaign_error", "could not start campaign")
	}
}

func validRecipientStatus(status string) bool {
	switch status {
	case domain.RecipientPending, domain.RecipientSent, domain.RecipientFailed, domain.RecipientSkipped:
		return true
	default:
		return false
	}
}

func parseRecipientPage(r *http.Request) (int, int, error) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return 0, 0, errors.New("page must be a positive integer")
		}
		if parsed > campaignRecipientPages {
			return 0, 0, errors.New("page is too large")
		}
		page = parsed
	}
	pageSize := campaignRecipientPage
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return 0, 0, errors.New("page_size must be a positive integer")
		}
		pageSize = min(parsed, campaignRecipientMax)
	}
	return page, pageSize, nil
}

func (a *API) campaignRoutes(r chi.Router) {
	if a.campaigns == nil || a.campaignRunner == nil {
		return
	}
	r.Get("/campaigns", a.handleCampaignList)
	r.With(a.requireCSRF).Post("/campaigns", a.handleCampaignCreate)
	r.Get("/campaigns/{id}", a.handleCampaignGet)
	r.With(a.requireCSRF).Put("/campaigns/{id}", a.handleCampaignUpdate)
	r.With(a.requireCSRF).Delete("/campaigns/{id}", a.handleCampaignDelete)
	r.Get("/campaigns/{id}/recipients", a.handleCampaignRecipientsList)
	r.With(a.requireCSRF).Post("/campaigns/{id}/recipients", a.handleCampaignRecipientsImport)
	r.With(a.requireCSRF).Post("/campaigns/{id}/start", a.handleCampaignStart)
	r.With(a.requireCSRF).Post("/campaigns/{id}/pause", a.handleCampaignPause)
}
