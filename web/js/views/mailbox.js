import { useState, useEffect, useRef } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { Link } from "../router.js";
import { api } from "../api.js";
import {
  Button,
  EmptyState,
  ErrorBox,
  LoadingRow,
  Modal,
  Spinner,
  toast,
} from "../components/ui.js";

const MAX_ROWS = 200;
const PAGE_SIZES = [25, 50, 100];

const ACTION_LABELS = {
  mark_read: "Mark read",
  mark_unread: "Mark unread",
  archive: "Archive",
  trash: "Trash",
};

function formatDate(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function isConnectError(error) {
  return error && (error.code === "google_not_connected" || error.code === "google_not_configured");
}

export function Mailbox() {
  const [query, setQuery] = useState("");
  const [label, setLabel] = useState("");
  const [unread, setUnread] = useState(false);
  const [filters, setFilters] = useState({ q: "", label: "", unread: false });
  const [pageSize, setPageSize] = useState(25);
  const [stack, setStack] = useState([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [reloadKey, setReloadKey] = useState(0);

  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [selected, setSelected] = useState([]);
  const [busy, setBusy] = useState(false);
  const [confirmTrash, setConfirmTrash] = useState(false);

  const [detail, setDetail] = useState(null);
  const requestSeq = useRef(0);

  useEffect(() => {
    const seq = requestSeq.current + 1;
    requestSeq.current = seq;
    setLoading(true);
    setError(null);
    setSelected([]);
    api
      .mailList({
        q: filters.q,
        label: filters.label,
        unread: filters.unread,
        pageToken: stack[pageIndex] || "",
        pageSize,
      })
      .then((next) => {
        if (requestSeq.current === seq) setData(next);
      })
      .catch((err) => {
        if (requestSeq.current === seq) {
          setError(err);
          setData(null);
        }
      })
      .finally(() => {
        if (requestSeq.current === seq) setLoading(false);
      });
  }, [filters, stack, pageIndex, pageSize, reloadKey]);

  const items = (data && data.items ? data.items : []).slice(0, MAX_ROWS);
  const allSelected = items.length > 0 && items.every((item) => selected.includes(item.id));
  const hasNext = Boolean(data && data.next_page_token);
  const pageNumber = pageIndex + 1;

  function applyFilters(event) {
    event.preventDefault();
    setFilters({ q: query.trim(), label: label.trim(), unread });
    setStack([""]);
    setPageIndex(0);
    setData(null);
    setError(null);
  }

  function clearFilters() {
    setQuery("");
    setLabel("");
    setUnread(false);
    setFilters({ q: "", label: "", unread: false });
    setStack([""]);
    setPageIndex(0);
    setData(null);
    setError(null);
  }

  function refresh() {
    setReloadKey((value) => value + 1);
  }

  function goNext() {
    if (!hasNext) return;
    const nextToken = data.next_page_token;
    setStack((current) => {
      const copy = current.slice(0, pageIndex + 1);
      copy[pageIndex + 1] = nextToken;
      return copy;
    });
    setPageIndex((value) => value + 1);
  }

  function goPrev() {
    if (pageIndex === 0) return;
    setPageIndex((value) => value - 1);
  }

  function changePageSize(value) {
    setPageSize(value);
    setStack([""]);
    setPageIndex(0);
  }

  function toggleSelected(id) {
    setSelected((current) =>
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id],
    );
  }

  function toggleAll() {
    if (allSelected) {
      setSelected((current) => current.filter((id) => !items.some((item) => item.id === id)));
      return;
    }
    const visible = items.map((item) => item.id);
    setSelected((current) => Array.from(new Set([...current, ...visible])));
  }

  async function executeBulk(action, confirm) {
    if (selected.length === 0 || busy) return;
    setBusy(true);
    try {
      const result = await api.mailBulk(selected, action, confirm);
      toast(`${ACTION_LABELS[action]} applied to ${result.affected} message${result.affected === 1 ? "" : "s"}`, "success");
      setSelected([]);
      setConfirmTrash(false);
      refresh();
    } catch (err) {
      toast(err.message || "Bulk action failed", "error");
      setConfirmTrash(false);
    } finally {
      setBusy(false);
    }
  }

  function runBulk(action) {
    if (action === "trash") {
      setConfirmTrash(true);
      return;
    }
    executeBulk(action, false);
  }

  async function openMessage(id) {
    setDetail({ id, loading: true });
    try {
      const message = await api.mailGet(id);
      setDetail({ id, message });
    } catch (err) {
      setDetail({ id, error: err.message || "Could not load message" });
    }
  }

  function renderRows() {
    if (loading) return html`<${LoadingRow} label="Loading messages…" />`;
    if (error) {
      if (isConnectError(error)) {
        return html`
          <${EmptyState} icon="mailbox" title="Google account not connected">
            Connect your Gmail account in${" "}
            <${Link} href="/settings">Settings</${Link}> to browse the mailbox.
          </${EmptyState}>
        `;
      }
      return html`
        <div class="empty">
          <div class="empty-title">Could not load messages</div>
          <p>${error.message || "Gmail request failed"}</p>
          <div class="row" style=${{ justifyContent: "center", marginTop: "12px" }}>
            <${Button} variant="secondary" size="sm" onClick=${refresh}>Try again</${Button}>
          </div>
        </div>
      `;
    }
    if (items.length === 0) {
      return html`
        <${EmptyState} icon="mailbox" title="No messages found">
          Try a different search or clear the filters.
        </${EmptyState}>
      `;
    }
    return html`
      <table class="table mail-table">
        <thead>
          <tr>
            <th class="check-col">
              <input
                type="checkbox"
                class="row-check"
                checked=${allSelected}
                onChange=${toggleAll}
                aria-label="Select all messages on this page"
              />
            </th>
            <th>From</th>
            <th>Subject</th>
            <th class="nowrap">Date</th>
          </tr>
        </thead>
        <tbody>
          ${items.map(
            (message) => html`
              <tr
                key=${message.id}
                class=${`message-row ${message.unread ? "message-unread" : ""}`}
                onClick=${() => openMessage(message.id)}
              >
                <td class="check-col" onClick=${(event) => event.stopPropagation()}>
                  <input
                    type="checkbox"
                    class="row-check"
                    checked=${selected.includes(message.id)}
                    onChange=${() => toggleSelected(message.id)}
                    aria-label=${`Select ${message.subject || "message"}`}
                  />
                </td>
                <td>
                  <div class="message-from">${message.from || "(unknown sender)"}</div>
                </td>
                <td>
                  <div class="message-subject truncate">${message.subject || "(no subject)"}</div>
                  <div class="snippet">${message.snippet || ""}</div>
                </td>
                <td class="nowrap muted small">${formatDate(message.date)}</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
    `;
  }

  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Mailbox</h1>
          <p class="page-sub">Search and manage messages from your Gmail account.</p>
        </div>
        <div class="page-actions">
          <${Button} variant="secondary" size="sm" onClick=${refresh} disabled=${loading}>
            ${loading ? html`<${Spinner} />` : "Refresh"}
          </${Button}>
        </div>
      </div>

      <form class="card mail-toolbar" onSubmit=${applyFilters}>
        <div class="field mail-search">
          <label class="label" for="mail-q">Search</label>
          <input
            id="mail-q"
            class="input"
            placeholder="Gmail syntax, e.g. from:alice has:attachment"
            value=${query}
            onInput=${(event) => setQuery(event.target.value)}
          />
        </div>
        <div class="field mail-label">
          <label class="label" for="mail-label">Label</label>
          <input
            id="mail-label"
            class="input"
            placeholder="e.g. INBOX"
            value=${label}
            onInput=${(event) => setLabel(event.target.value)}
          />
        </div>
        <label class="checkbox-row mail-unread">
          <input
            type="checkbox"
            checked=${unread}
            onChange=${(event) => setUnread(event.target.checked)}
          />
          Unread only
        </label>
        <div class="row mail-toolbar-actions">
          <${Button} type="submit" variant="primary">Search</${Button}>
          <${Button} type="button" variant="ghost" onClick=${clearFilters}>Clear</${Button}>
        </div>
      </form>

      ${selected.length > 0 &&
      html`
        <div class="bulk-bar" role="region" aria-label="Bulk actions">
          <span><strong>${selected.length}</strong> selected</span>
          <div class="row">
            ${["mark_read", "mark_unread", "archive"].map(
              (action) => html`
                <${Button}
                  key=${action}
                  variant="secondary"
                  size="sm"
                  disabled=${busy}
                  onClick=${() => runBulk(action)}
                >
                  ${ACTION_LABELS[action]}
                </${Button}>
              `,
            )}
            <${Button} variant="danger-outline" size="sm" disabled=${busy} onClick=${() => runBulk("trash")}>
              Trash
            </${Button}>
          </div>
        </div>
      `}

      <div class=${`table-wrap mail-table-wrap ${items.length > 0 ? "has-rows" : ""}`}>${renderRows()}</div>

      <div class="pagination">
        <span>
          ${data && data.estimated_total
            ? `~${data.estimated_total} matching messages`
            : loading
              ? "Loading…"
              : "Cursor pagination"}
        </span>
        <div class="pagination-actions">
          <label class="tiny muted" for="mail-page-size">Rows</label>
          <select
            id="mail-page-size"
            value=${String(pageSize)}
            onChange=${(event) => changePageSize(Number(event.target.value))}
          >
            ${PAGE_SIZES.map((size) => html`<option value=${String(size)}>${size}</option>`)}
          </select>
          <${Button} variant="secondary" size="sm" disabled=${pageIndex === 0 || loading} onClick=${goPrev}>
            Prev
          </${Button}>
          <span class="tiny muted">Page ${pageNumber}</span>
          <${Button} variant="secondary" size="sm" disabled=${!hasNext || loading} onClick=${goNext}>
            Next
          </${Button}>
        </div>
      </div>

      ${confirmTrash &&
      html`
        <${Modal}
          title="Move messages to Trash"
          onClose=${() => setConfirmTrash(false)}
          footer=${html`
            <${Button} variant="secondary" onClick=${() => setConfirmTrash(false)}>Cancel</${Button}>
            <${Button} variant="danger" disabled=${busy} onClick=${() => executeBulk("trash", true)}>
              ${busy ? "Moving…" : "Move to Trash"}
            </${Button}>
          `}
        >
          <p>
            Move <strong>${selected.length}</strong> selected message${selected.length === 1 ? "" : "s"} to Trash?
          </p>
          <p class="hint">Messages in Trash are permanently removed by Google after 30 days.</p>
        </${Modal}>
      `}

      ${detail &&
      html`
        <${Modal}
          title="Message"
          className="modal-wide"
          onClose=${() => setDetail(null)}
          footer=${html`<${Button} variant="secondary" onClick=${() => setDetail(null)}>Close</${Button}>`}
        >
          ${detail.loading
            ? html`<${LoadingRow} label="Loading message…" />`
            : detail.error
              ? html`<${ErrorBox} message=${detail.error} />`
              : html`
                  <div class="mail-detail-head">
                    <h3 class="mail-detail-subject">${detail.message.subject || "(no subject)"}</h3>
                    <dl class="mail-meta">
                      <dt>From</dt>
                      <dd>${detail.message.from || "—"}</dd>
                      <dt>To</dt>
                      <dd>${detail.message.to || "—"}</dd>
                      <dt>Date</dt>
                      <dd>${formatDate(detail.message.date) || "—"}</dd>
                    </dl>
                    ${detail.message.unread && html`<span class="badge badge-blue">Unread</span>`}
                  </div>
                  ${detail.message.body_text
                    ? html`<pre class="mail-body">${detail.message.body_text}</pre>`
                    : html`
                        <p class="muted small">
                          This message has no plain-text body. Open it in Gmail to view the formatted content.
                        </p>
                      `}
                  <${MessageAssistant} message=${detail.message} />
                `}
        </${Modal}>
      `}
    </div>
  `;
}

function aiErrorMessage(error) {
  if (!error) return "";
  if (error.code === "llm_not_configured") {
    return "Configure an AI provider in Settings to use the assistant.";
  }
  if (error.code === "google_not_connected" || error.code === "google_not_configured") {
    return "Connect your Google account before using the assistant.";
  }
  return error.message || "The AI request failed.";
}

function MessageAssistant({ message }) {
  const [summary, setSummary] = useState("");
  const [summaryBusy, setSummaryBusy] = useState(false);
  const [summaryError, setSummaryError] = useState("");
  const [draftOpen, setDraftOpen] = useState(false);
  const [instructions, setInstructions] = useState("");
  const [draft, setDraft] = useState(null);
  const [draftBusy, setDraftBusy] = useState(false);
  const [draftError, setDraftError] = useState("");
  const summarySeq = useRef(0);
  const draftSeq = useRef(0);

  async function summarize() {
    if (summaryBusy) return;
    const seq = summarySeq.current + 1;
    summarySeq.current = seq;
    setSummaryBusy(true);
    setSummaryError("");
    try {
      const result = await api.summarize([message.id]);
      if (summarySeq.current !== seq) return;
      setSummary(result.summary || "");
    } catch (err) {
      if (summarySeq.current === seq) setSummaryError(aiErrorMessage(err));
    } finally {
      if (summarySeq.current === seq) setSummaryBusy(false);
    }
  }

  async function generateDraft() {
    if (draftBusy) return;
    const seq = draftSeq.current + 1;
    draftSeq.current = seq;
    setDraftBusy(true);
    setDraftError("");
    try {
      const result = await api.draftReply(message.id, instructions.trim());
      if (draftSeq.current !== seq) return;
      setDraft(result);
    } catch (err) {
      if (draftSeq.current === seq) setDraftError(aiErrorMessage(err));
    } finally {
      if (draftSeq.current === seq) setDraftBusy(false);
    }
  }

  async function copyDraft() {
    if (!draft) return;
    const text = `To: ${draft.to || ""}\nSubject: ${draft.subject || ""}\n\n${draft.body || ""}`;
    try {
      await navigator.clipboard.writeText(text);
      toast("Draft copied", "success");
    } catch {
      toast("Could not copy the draft", "error");
    }
  }

  return html`
    <div class="ai-panel">
      <div class="ai-panel-head">
        <div>
          <h4 class="ai-title">AI assistant</h4>
          <p class="tiny muted">
            Read-only: summaries and drafts never change or send email.
            <${Link} href="/settings" class="ai-settings-link">Provider settings</${Link}>
          </p>
        </div>
        <div class="ai-actions">
          <${Button} variant="secondary" size="sm" onClick=${summarize} disabled=${summaryBusy}>
            ${summaryBusy ? html`<${Spinner} /> Summarizing…` : "Summarize"}
          </${Button}>
          <${Button}
            variant="secondary"
            size="sm"
            onClick=${() => {
              setDraftOpen((value) => !value);
              setDraftError("");
            }}
          >
            ${draftOpen ? "Hide draft" : "Draft reply"}
          </${Button}>
        </div>
      </div>

      <${ErrorBox} message=${summaryError} />
      ${summary &&
      html`
        <div class="ai-output">
          <div class="label">Summary</div>
          <div class="ai-summary">${summary}</div>
        </div>
      `}

      ${draftOpen &&
      html`
        <div class="ai-draft">
          <div class="field">
            <label class="label" for="ai-instructions">Instructions (optional)</label>
            <textarea
              id="ai-instructions"
              class="textarea ai-instructions"
              maxlength="2000"
              placeholder="Keep it short and confirm the meeting time."
              value=${instructions}
              onInput=${(event) => setInstructions(event.target.value)}
              disabled=${draftBusy}
            ></textarea>
          </div>
          <div class="row">
            <${Button} variant="primary" size="sm" onClick=${generateDraft} disabled=${draftBusy}>
              ${draftBusy ? html`<${Spinner} /> Drafting…` : "Generate draft"}
            </${Button}>
          </div>
          <${ErrorBox} message=${draftError} />
          ${draft &&
          html`
            <div class="ai-draft-result">
              <div class="ai-draft-meta">
                <span class="tiny muted">Draft only — MailController will not send it.</span>
              </div>
              <div class="field">
                <span class="label">To</span>
                <div class="ai-draft-line mono">${draft.to || "—"}</div>
              </div>
              <div class="field">
                <span class="label">Subject</span>
                <div class="ai-draft-line">${draft.subject || "—"}</div>
              </div>
              <div class="field">
                <span class="label">Body</span>
                <pre class="ai-draft-body">${draft.body}</pre>
              </div>
              <div class="row">
                <${Button} variant="secondary" size="sm" onClick=${copyDraft}>Copy draft</${Button}>
              </div>
            </div>
          `}
        </div>
      `}
    </div>
  `;
}
