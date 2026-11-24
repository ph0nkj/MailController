import { useState, useEffect, useRef } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { api } from "../api.js";
import {
  Button,
  EmptyState,
  ErrorBox,
  LoadingRow,
  Modal,
  Pagination,
  ProgressBar,
  Spinner,
  toast,
} from "../components/ui.js";

const MAX_ROWS = 200;
const PAGE_SIZES = [50, 100, 200];
const IMPORT_LIMIT = 16 * 1024 * 1024;
const IMPORT_PLACEHOLDER = `alice@example.com
Bob <bob@example.com>
email,name
carol@example.com,Carol`;

const STATUS_META = {
  draft: { label: "Draft", badge: "badge-gray" },
  running: { label: "Running", badge: "badge-blue" },
  paused: { label: "Paused", badge: "badge-amber" },
  done: { label: "Done", badge: "badge-green" },
  failed: { label: "Failed", badge: "badge-red" },
};

const RECIPIENT_STATUS_META = {
  pending: { label: "Pending", badge: "badge-gray" },
  sent: { label: "Sent", badge: "badge-green" },
  failed: { label: "Failed", badge: "badge-red" },
  skipped: { label: "Skipped", badge: "badge-amber" },
};

function formatDate(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function StatusBadge({ status }) {
  const meta = STATUS_META[status] || { label: status || "Unknown", badge: "badge-gray" };
  return html`<span class=${`badge ${meta.badge}`}>${meta.label}</span>`;
}

function RecipientBadge({ status }) {
  const meta = RECIPIENT_STATUS_META[status] || { label: status || "Unknown", badge: "badge-gray" };
  return html`<span class=${`badge ${meta.badge}`}>${meta.label}</span>`;
}

function processedCount(stats) {
  if (!stats) return 0;
  return (stats.sent || 0) + (stats.failed || 0) + (stats.skipped || 0);
}

function Stat({ label, value, note, tone }) {
  return html`
    <div class="stat">
      <div class="stat-label">${label}</div>
      <div class=${`stat-value ${tone === "small" ? "stat-value-sm" : ""}`}>${value}</div>
      ${note && html`<div class="stat-note">${note}</div>`}
    </div>
  `;
}

export function Campaigns() {
  const [selectedId, setSelectedId] = useState(null);
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    let active = true;
    if (!data) setLoading(true);
    setError("");
    api
      .campaigns()
      .then((next) => {
        if (active) setData(next);
      })
      .catch((err) => {
        if (active) setError(err.message || "Could not load campaigns");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [reloadKey]);

  const items = data && Array.isArray(data.items) ? data.items.slice(0, MAX_ROWS) : [];
  const hasRunning = items.some((campaign) => campaign.status === "running");

  useEffect(() => {
    if (!hasRunning) return undefined;
    const timer = window.setInterval(() => setReloadKey((value) => value + 1), 5000);
    return () => window.clearInterval(timer);
  }, [hasRunning]);

  function openCampaign(id) {
    setSelectedId(id);
  }

  function backToList() {
    setSelectedId(null);
    setReloadKey((value) => value + 1);
  }

  if (selectedId) {
    return html`
      <${CampaignDetail} id=${selectedId} onBack=${backToList} onChanged=${() => setReloadKey((value) => value + 1)} />
    `;
  }

  function renderList() {
    if (loading && !data) {
      return html`<div class="card"><${LoadingRow} label="Loading campaigns…" /></div>`;
    }
    if (error && !data) {
      return html`
        <div class="card">
          <${ErrorBox} message=${error} />
          <div class="row">
            <${Button} variant="secondary" size="sm" onClick=${() => setReloadKey((value) => value + 1)}>
              Try again
            </${Button}>
          </div>
        </div>
      `;
    }
    if (items.length === 0) {
      return html`
        <div class="card">
          <${EmptyState} icon="campaigns" title="No campaigns yet">
            Create a draft, import recipients and review everything before the first message is sent.
          </${EmptyState}>
          <div class="row" style=${{ justifyContent: "center" }}>
            <${Button} variant="primary" onClick=${() => setCreating(true)}>Create campaign</${Button}>
          </div>
        </div>
      `;
    }
    return html`
      <div class="table-wrap">
        <table class="table campaign-table">
          <thead>
            <tr>
              <th>Campaign</th>
              <th>Status</th>
              <th class="nowrap">Recipients</th>
              <th class="campaign-progress-col">Progress</th>
              <th class="nowrap">Created</th>
            </tr>
          </thead>
          <tbody>
            ${items.map((campaign) => {
              const processed = processedCount(campaign.stats);
              const total = campaign.stats ? campaign.stats.total : 0;
              return html`
                <tr key=${campaign.id} class="campaign-row" onClick=${() => openCampaign(campaign.id)}>
                  <td>
                    <div class="campaign-name">${campaign.name}</div>
                    <div class="campaign-subject truncate">${campaign.subject || "(no subject)"}</div>
                  </td>
                  <td><${StatusBadge} status=${campaign.status} /></td>
                  <td class="nowrap">${total.toLocaleString()}</td>
                  <td class="campaign-progress-col">
                    <${ProgressBar} value=${processed} max=${total} />
                    <span class="tiny muted">${processed.toLocaleString()} of ${total.toLocaleString()} processed</span>
                  </td>
                  <td class="nowrap muted small">${formatDate(campaign.created_at)}</td>
                </tr>
              `;
            })}
          </tbody>
        </table>
      </div>
    `;
  }

  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Campaigns</h1>
          <p class="page-sub">Send to large recipient lists with throttling, daily limits and progress.</p>
        </div>
        <div class="page-actions">
          <${Button} variant="primary" onClick=${() => setCreating(true)}>New campaign</${Button}>
        </div>
      </div>

      ${error && data && html`<${ErrorBox} message=${error} />`}
      ${renderList()}
      <p class="hint campaign-list-hint">
        Showing up to ${MAX_ROWS} campaigns. Open a campaign to import recipients and review delivery.
      </p>

      ${creating &&
      html`
        <${CampaignEditor}
          onClose=${() => setCreating(false)}
          onSaved=${(saved) => {
            setCreating(false);
            setSelectedId(saved.id);
            setReloadKey((value) => value + 1);
          }}
        />
      `}
    </div>
  `;
}

function CampaignDetail({ id, onBack, onChanged }) {
  const [detail, setDetail] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [recipients, setRecipients] = useState(null);
  const [recipientsLoading, setRecipientsLoading] = useState(true);
  const [recipientsError, setRecipientsError] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [refreshKey, setRefreshKey] = useState(0);
  const [editing, setEditing] = useState(false);
  const [importText, setImportText] = useState("");
  const [importResult, setImportResult] = useState(null);
  const [importError, setImportError] = useState("");
  const [importing, setImporting] = useState(false);
  const [confirmStart, setConfirmStart] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [actionBusy, setActionBusy] = useState("");
  const [actionError, setActionError] = useState("");
  const detailSeq = useRef(0);
  const recipientsSeq = useRef(0);

  function refresh() {
    setRefreshKey((value) => value + 1);
  }

  function loadDetail({ quiet = false } = {}) {
    const seq = detailSeq.current + 1;
    detailSeq.current = seq;
    if (!quiet) setLoading(true);
    return api
      .campaign(id)
      .then((next) => {
        if (detailSeq.current !== seq) return;
        setDetail(next);
        setLoadError("");
      })
      .catch((err) => {
        if (detailSeq.current !== seq) return;
        if (!quiet) setLoadError(err.message || "Could not load campaign");
      })
      .finally(() => {
        if (detailSeq.current === seq && !quiet) setLoading(false);
      });
  }

  function loadRecipients({ quiet = false } = {}) {
    const seq = recipientsSeq.current + 1;
    recipientsSeq.current = seq;
    if (!quiet) setRecipientsLoading(true);
    return api
      .campaignRecipients(id, { status: statusFilter, page, pageSize })
      .then((next) => {
        if (recipientsSeq.current !== seq) return;
        setRecipients(next);
        setRecipientsError("");
      })
      .catch((err) => {
        if (recipientsSeq.current !== seq) return;
        if (!quiet) setRecipientsError(err.message || "Could not load recipients");
      })
      .finally(() => {
        if (recipientsSeq.current === seq && !quiet) setRecipientsLoading(false);
      });
  }

  useEffect(() => {
    loadDetail();
    return () => {
      detailSeq.current += 1;
    };
  }, [id, refreshKey]);

  useEffect(() => {
    loadRecipients();
    return () => {
      recipientsSeq.current += 1;
    };
  }, [id, statusFilter, page, pageSize, refreshKey]);

  const running = Boolean(detail && detail.status === "running");
  useEffect(() => {
    if (!running) return undefined;
    const timer = window.setInterval(() => {
      loadDetail({ quiet: true });
      loadRecipients({ quiet: true });
    }, 3000);
    return () => window.clearInterval(timer);
  }, [running, id, statusFilter, page, pageSize]);

  function changeFilter(event) {
    setStatusFilter(event.target.value);
    setPage(1);
  }

  function changePageSize(next) {
    setPageSize(next);
    setPage(1);
  }

  async function importRecipients() {
    if (importing || !importText.trim()) return;
    setImporting(true);
    setImportError("");
    setImportResult(null);
    try {
      const result = await api.importCampaignRecipients(id, importText);
      setImportResult(result);
      setImportText("");
      setPage(1);
      refresh();
      onChanged();
      toast(
        `Imported ${result.imported.toLocaleString()} recipient${result.imported === 1 ? "" : "s"}`,
        "success",
      );
    } catch (err) {
      setImportError(err.message || "Could not import recipients");
    } finally {
      setImporting(false);
    }
  }

  async function chooseFile(event) {
    const input = event.target;
    const file = input.files && input.files[0];
    input.value = "";
    if (!file) return;
    if (file.size > IMPORT_LIMIT) {
      setImportError("Recipient files are limited to 16 MiB.");
      return;
    }
    try {
      const text = await file.text();
      setImportText(text);
      setImportError("");
      setImportResult(null);
    } catch {
      setImportError("Could not read the selected file.");
    }
  }

  async function startCampaign() {
    if (actionBusy) return;
    setActionBusy("start");
    setActionError("");
    try {
      await api.startCampaign(id);
      setConfirmStart(false);
      refresh();
      onChanged();
      toast(detail && detail.status === "paused" ? "Campaign resumed" : "Campaign started", "success");
    } catch (err) {
      setActionError(err.message || "Could not start campaign");
    } finally {
      setActionBusy("");
    }
  }

  async function pauseCampaign() {
    if (actionBusy) return;
    setActionBusy("pause");
    setActionError("");
    try {
      await api.pauseCampaign(id);
      refresh();
      onChanged();
      toast("Campaign paused", "success");
    } catch (err) {
      toast(err.message || "Could not pause campaign", "error");
    } finally {
      setActionBusy("");
    }
  }

  async function deleteCampaign() {
    if (actionBusy) return;
    setActionBusy("delete");
    setActionError("");
    try {
      await api.deleteCampaign(id);
      setConfirmDelete(false);
      toast("Campaign deleted", "success");
      onChanged();
      onBack();
    } catch (err) {
      setActionError(err.message || "Could not delete campaign");
    } finally {
      setActionBusy("");
    }
  }

  if (loading && !detail) {
    return html`
      <div>
        <div class="page-head">
          <div>
            <button class="link-button campaign-back" type="button" onClick=${onBack}>← Campaigns</button>
            <h1 class="page-title">Campaign</h1>
          </div>
        </div>
        <div class="card"><${LoadingRow} label="Loading campaign…" /></div>
      </div>
    `;
  }

  if (loadError && !detail) {
    return html`
      <div>
        <div class="page-head">
          <div>
            <button class="link-button campaign-back" type="button" onClick=${onBack}>← Campaigns</button>
            <h1 class="page-title">Campaign</h1>
          </div>
        </div>
        <div class="card">
          <${ErrorBox} message=${loadError} />
          <div class="row">
            <${Button} variant="secondary" size="sm" onClick=${refresh}>Try again</${Button}>
            <${Button} variant="ghost" size="sm" onClick=${onBack}>Back to campaigns</${Button}>
          </div>
        </div>
      </div>
    `;
  }

  if (!detail) return null;

  const stats = detail.stats || { total: 0, pending: 0, sent: 0, failed: 0, skipped: 0 };
  const processed = processedCount(stats);
  const recipientItems = (recipients && recipients.items ? recipients.items : []).slice(0, MAX_ROWS);
  const totalRecipients = recipients ? recipients.total : stats.total;
  const isDraft = detail.status === "draft";
  const canStart = (detail.status === "draft" || detail.status === "paused") && stats.pending > 0;

  function renderRecipients() {
    if (recipientsLoading && !recipients) {
      return html`<${LoadingRow} label="Loading recipients…" />`;
    }
    if (recipientsError && !recipients) {
      return html`
        <${ErrorBox} message=${recipientsError} />
        <div class="row">
          <${Button} variant="secondary" size="sm" onClick=${() => loadRecipients()}>Try again</${Button}>
        </div>
      `;
    }
    if (recipientItems.length === 0) {
      return html`
        <${EmptyState} icon="mailbox" title=${statusFilter ? "No recipients with this status" : "No recipients yet"}>
          ${statusFilter
            ? "Choose another status filter or import more addresses."
            : isDraft
              ? "Paste addresses or upload a CSV file above."
              : "This campaign has no recipients."}
        </${EmptyState}>
      `;
    }
    return html`
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>Email</th>
              <th>Status</th>
              <th>Error</th>
              <th class="nowrap">Sent at</th>
            </tr>
          </thead>
          <tbody>
            ${recipientItems.map(
              (recipient) => html`
                <tr key=${recipient.id}>
                  <td class="recipient-email">${recipient.email}</td>
                  <td><${RecipientBadge} status=${recipient.status} /></td>
                  <td class="recipient-error small muted">${recipient.error || "—"}</td>
                  <td class="nowrap muted small">${formatDate(recipient.sent_at)}</td>
                </tr>
              `,
            )}
          </tbody>
        </table>
      </div>
      <${Pagination}
        page=${page}
        pageSize=${pageSize}
        total=${totalRecipients}
        pageSizes=${PAGE_SIZES}
        onPage=${(next) => setPage(next)}
        onPageSize=${changePageSize}
      />
    `;
  }

  return html`
    <div>
      <div class="page-head">
        <div class="campaign-detail-heading">
          <button class="link-button campaign-back" type="button" onClick=${onBack}>← Campaigns</button>
          <h1 class="page-title">${detail.name}</h1>
          <p class="page-sub campaign-subject-line">${detail.subject || "(no subject)"}</p>
        </div>
        <div class="page-actions">
          <${StatusBadge} status=${detail.status} />
          <${Button} variant="ghost" size="sm" onClick=${refresh} disabled=${loading}>
            ${loading ? html`<${Spinner} />` : "Refresh"}
          </${Button}>
          ${isDraft &&
          html`
            <${Button} variant="secondary" size="sm" onClick=${() => setEditing(true)}>Edit</${Button}>
            <${Button} variant="danger-outline" size="sm" onClick=${() => setConfirmDelete(true)}>Delete</${Button}>
          `}
          ${detail.status === "running" &&
          html`
            <${Button} variant="secondary" size="sm" disabled=${actionBusy === "pause"} onClick=${pauseCampaign}>
              ${actionBusy === "pause" ? "Pausing…" : "Pause"}
            </${Button}>
          `}
          ${canStart &&
          html`
            <${Button} variant="primary" size="sm" onClick=${() => setConfirmStart(true)}>
              ${detail.status === "paused" ? "Resume sending" : "Start sending"}
            </${Button}>
          `}
        </div>
      </div>

      <div class="stack">
        <div class="stat-grid campaign-stats">
          <${Stat} label="Recipients" value=${stats.total.toLocaleString()} note="Imported addresses" />
          <${Stat} label="Sent" value=${stats.sent.toLocaleString()} note="Delivered successfully" />
          <${Stat} label="Pending" value=${stats.pending.toLocaleString()} note="Waiting in the queue" />
          <${Stat} label="Failed" value=${stats.failed.toLocaleString()} note="Could not be delivered" />
          <${Stat} label="Skipped" value=${stats.skipped.toLocaleString()} note="Excluded from sending" />
        </div>

        <section class="card">
          <div class="row-between campaign-progress-head">
            <div>
              <h2 class="card-title">Delivery progress</h2>
              <p class="card-sub">
                ${detail.status === "running"
                  ? "Sending now. Progress refreshes automatically every few seconds."
                  : "Progress across all recipients in this campaign."}
              </p>
            </div>
            <span class="tiny muted">${detail.daily_limit.toLocaleString()} per day · ${detail.rate_per_min}/min</span>
          </div>
          <${ProgressBar}
            value=${processed}
            max=${stats.total}
            label=${`${processed.toLocaleString()} of ${stats.total.toLocaleString()} recipients processed`}
          />
          <div class="campaign-facts">
            <span>Created ${formatDate(detail.created_at)}</span>
            ${detail.started_at && html`<span>Started ${formatDate(detail.started_at)}</span>`}
            ${detail.finished_at && html`<span>Finished ${formatDate(detail.finished_at)}</span>`}
          </div>
        </section>

        <section class="card">
          <h2 class="card-title">Message</h2>
          <p class="card-sub">
            Use <span class="mono">{{email}}</span> anywhere in the subject or body; it is replaced
            with each recipient address.
          </p>
          <div class="campaign-message-subject">
            <span class="label">Subject</span>
            <div>${detail.subject}</div>
          </div>
          ${detail.body_text
            ? html`<pre class="campaign-body">${detail.body_text}</pre>`
            : html`<p class="hint">No plain-text body. A short fallback is generated for HTML-only messages.</p>`}
          ${detail.body_html &&
          html`
            <p class="hint">
              HTML body included (${detail.body_html.length.toLocaleString()} characters). It is sent as the
              formatted version alongside the plain-text body.
            </p>
          `}
        </section>

        ${isDraft &&
        html`
          <section class="card">
            <h2 class="card-title">Import recipients</h2>
            <p class="card-sub">
              Paste addresses or upload a plain text/CSV file. One address per line, or a CSV with an
              optional <span class="mono">Email</span> column. Up to 50,000 recipients per campaign.
            </p>
            <div class="import-grid">
              <div class="field import-paste">
                <label class="label" for="campaign-import">Addresses</label>
                <textarea
                  id="campaign-import"
                  class="textarea textarea-lg mono"
                  placeholder=${IMPORT_PLACEHOLDER}
                  value=${importText}
                  onInput=${(event) => setImportText(event.target.value)}
                  disabled=${importing}
                ></textarea>
              </div>
              <div class="import-side">
                <div class="field">
                  <label class="label" for="campaign-import-file">Upload file</label>
                  <input
                    id="campaign-import-file"
                    class="file-input"
                    type="file"
                    accept=".txt,.csv,text/plain,text/csv"
                    onChange=${chooseFile}
                    disabled=${importing}
                  />
                  <p class="hint">Plain text or CSV, up to 16 MiB.</p>
                </div>
                <${Button}
                  variant="primary"
                  onClick=${importRecipients}
                  disabled=${importing || !importText.trim()}
                >
                  ${importing ? html`<${Spinner} /> Importing…` : "Import recipients"}
                </${Button}>
                ${importText &&
                html`<p class="tiny muted">${importText.length.toLocaleString()} characters ready to import.</p>`}
              </div>
            </div>
            <${ErrorBox} message=${importError} />
            ${importResult &&
            html`
              <div class="import-summary">
                <span><strong>${importResult.imported.toLocaleString()}</strong> added</span>
                <span><strong>${importResult.duplicates.toLocaleString()}</strong> duplicates</span>
                <span><strong>${importResult.invalid.toLocaleString()}</strong> invalid</span>
                <span>Total: <strong>${importResult.total.toLocaleString()}</strong></span>
              </div>
              ${importResult.invalid_samples && importResult.invalid_samples.length > 0 &&
              html`
                <details class="import-samples">
                  <summary>Rejected samples</summary>
                  <ul>
                    ${importResult.invalid_samples.map(
                      (sample) => html`<li key=${sample} class="mono small">${sample}</li>`,
                    )}
                  </ul>
                </details>
              `}
            `}
          </section>
        `}

        <section class="card">
          <div class="row-between campaign-recipients-head">
            <div>
              <h2 class="card-title">Recipients</h2>
              <p class="card-sub">
                ${totalRecipients.toLocaleString()} recipient${totalRecipients === 1 ? "" : "s"}
                ${statusFilter ? ` with status ${STATUS_META[statusFilter]?.label || statusFilter}` : ""}.
              </p>
            </div>
            <div class="row">
              <label class="label" for="campaign-status-filter">Status</label>
              <select
                id="campaign-status-filter"
                class="select campaign-filter"
                value=${statusFilter}
                onChange=${changeFilter}
              >
                <option value="">All</option>
                <option value="pending">Pending</option>
                <option value="sent">Sent</option>
                <option value="failed">Failed</option>
                <option value="skipped">Skipped</option>
              </select>
              <${Button} variant="secondary" size="sm" onClick=${() => loadRecipients()} disabled=${recipientsLoading}>
                ${recipientsLoading ? html`<${Spinner} />` : "Reload"}
              </${Button}>
            </div>
          </div>
          ${renderRecipients()}
        </section>
      </div>

      ${editing &&
      html`
        <${CampaignEditor}
          campaign=${detail}
          onClose=${() => setEditing(false)}
          onSaved=${(saved) => {
            setEditing(false);
            setDetail(saved);
            refresh();
            onChanged();
          }}
        />
      `}

      ${confirmStart &&
      html`
        <${Modal}
          title=${detail.status === "paused" ? "Resume campaign" : "Start sending campaign"}
          onClose=${() => {
            setConfirmStart(false);
            setActionError("");
          }}
          footer=${html`
            <${Button}
              variant="secondary"
              onClick=${() => {
                setConfirmStart(false);
                setActionError("");
              }}
              disabled=${actionBusy === "start"}
            >
              Cancel
            </${Button}>
            <${Button} variant="primary" onClick=${startCampaign} disabled=${actionBusy === "start"}>
              ${actionBusy === "start" ? "Starting…" : detail.status === "paused" ? "Resume sending" : "Start sending"}
            </${Button}>
          `}
        >
          <p>
            Send to <strong>${stats.pending.toLocaleString()}</strong> pending recipient${stats.pending === 1 ? "" : "s"}
            for <strong>${detail.name}</strong>?
          </p>
          <ul class="modal-list">
            <li>Daily limit: ${detail.daily_limit.toLocaleString()} messages.</li>
            <li>Rate: ${detail.rate_per_min} messages per minute.</li>
            <li>The campaign pauses automatically when a limit is reached.</li>
          </ul>
          <p class="hint">
            Real email is sent from the connected Gmail account. Review the subject and body before
            continuing; you can pause the campaign at any time.
          </p>
          <${ErrorBox} message=${actionError} />
        </${Modal}>
      `}

      ${confirmDelete &&
      html`
        <${Modal}
          title="Delete draft campaign"
          onClose=${() => {
            setConfirmDelete(false);
            setActionError("");
          }}
          footer=${html`
            <${Button}
              variant="secondary"
              onClick=${() => {
                setConfirmDelete(false);
                setActionError("");
              }}
              disabled=${actionBusy === "delete"}
            >
              Cancel
            </${Button}>
            <${Button} variant="danger" onClick=${deleteCampaign} disabled=${actionBusy === "delete"}>
              ${actionBusy === "delete" ? "Deleting…" : "Delete campaign"}
            </${Button}>
          `}
        >
          <p>
            Delete <strong>${detail.name}</strong> and its ${stats.total.toLocaleString()} recipient
            record${stats.total === 1 ? "" : "s"}?
          </p>
          <p class="hint">Only drafts can be deleted. This cannot be undone.</p>
          <${ErrorBox} message=${actionError} />
        </${Modal}>
      `}
    </div>
  `;
}

function CampaignEditor({ campaign, onClose, onSaved }) {
  const editing = Boolean(campaign);
  const [name, setName] = useState(campaign ? campaign.name : "");
  const [subject, setSubject] = useState(campaign ? campaign.subject : "");
  const [bodyText, setBodyText] = useState(campaign ? campaign.body_text || "" : "");
  const [bodyHTML, setBodyHTML] = useState(campaign ? campaign.body_html || "" : "");
  const [dailyLimit, setDailyLimit] = useState(String(campaign ? campaign.daily_limit : 500));
  const [ratePerMin, setRatePerMin] = useState(String(campaign ? campaign.rate_per_min : 60));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  async function save() {
    if (saving) return;
    setError("");
    const payload = {
      name: name.trim(),
      subject: subject.trim(),
      body_text: bodyText,
      body_html: bodyHTML,
      daily_limit: Number(dailyLimit) || 0,
      rate_per_min: Number(ratePerMin) || 0,
    };
    if (!payload.name || !payload.subject) {
      setError("A name and subject are required.");
      return;
    }
    if (!payload.body_text.trim() && !payload.body_html.trim()) {
      setError("Write a text or HTML body.");
      return;
    }
    setSaving(true);
    try {
      const saved = editing
        ? await api.updateCampaign(campaign.id, payload)
        : await api.createCampaign(payload);
      toast(editing ? "Campaign updated" : "Campaign created", "success");
      onSaved(saved);
    } catch (err) {
      setError(err.message || "Could not save the campaign");
    } finally {
      setSaving(false);
    }
  }

  return html`
    <${Modal}
      title=${editing ? "Edit campaign" : "New campaign"}
      className="modal-wide"
      onClose=${onClose}
      footer=${html`
        <${Button} variant="secondary" onClick=${onClose} disabled=${saving}>Cancel</${Button}>
        <${Button} variant="primary" onClick=${save} disabled=${saving}>
          ${saving ? html`<${Spinner} /> Saving…` : editing ? "Save changes" : "Create draft"}
        </${Button}>
      `}
    >
      <${ErrorBox} message=${error} />
      <div class="form-grid">
        <div class="field">
          <label class="label" for="campaign-name">Name</label>
          <input
            id="campaign-name"
            class="input"
            maxlength="120"
            placeholder="January product update"
            value=${name}
            onInput=${(event) => setName(event.target.value)}
            disabled=${saving}
          />
        </div>
        <div class="field">
          <label class="label" for="campaign-subject">Subject</label>
          <input
            id="campaign-subject"
            class="input"
            maxlength="300"
            placeholder="Hello {{email}}"
            value=${subject}
            onInput=${(event) => setSubject(event.target.value)}
            disabled=${saving}
          />
        </div>
        <div class="field">
          <label class="label" for="campaign-daily-limit">Daily limit</label>
          <input
            id="campaign-daily-limit"
            class="input"
            type="number"
            min="1"
            max="10000"
            value=${dailyLimit}
            onInput=${(event) => setDailyLimit(event.target.value)}
            disabled=${saving}
          />
          <p class="hint">Maximum recipients per rolling 24 hours (1–10,000).</p>
        </div>
        <div class="field">
          <label class="label" for="campaign-rate">Rate per minute</label>
          <input
            id="campaign-rate"
            class="input"
            type="number"
            min="1"
            max="600"
            value=${ratePerMin}
            onInput=${(event) => setRatePerMin(event.target.value)}
            disabled=${saving}
          />
          <p class="hint">Throttle between messages (1–600 per minute).</p>
        </div>
      </div>
      <div class="field">
        <label class="label" for="campaign-body-text">Text body</label>
        <textarea
          id="campaign-body-text"
          class="textarea"
          placeholder="Hi {{email}}, ..."
          value=${bodyText}
          onInput=${(event) => setBodyText(event.target.value)}
          disabled=${saving}
        ></textarea>
      </div>
      <div class="field">
        <label class="label" for="campaign-body-html">HTML body (optional)</label>
        <textarea
          id="campaign-body-html"
          class="textarea"
          placeholder="<p>Hi {{email}}, ...</p>"
          value=${bodyHTML}
          onInput=${(event) => setBodyHTML(event.target.value)}
          disabled=${saving}
        ></textarea>
        <p class="hint">
          HTML is sent as the rich version. Only the <span class="mono">{{email}}</span> placeholder is
          replaced; all other text is sent as written.
        </p>
      </div>
    </${Modal}>
  `;
}
