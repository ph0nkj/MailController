import { useState, useEffect, useRef } from "../vendor/hooks.mjs";
import { html } from "../html.js";

const iconPaths = {
  dashboard: "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z",
  mailbox: "M3 6.5h18v12H3zM3.5 7.5l8.5 6 8.5-6",
  assistant: "M12 3.5l1.7 4.8 4.8 1.7-4.8 1.7L12 16.5l-1.7-4.8L5.5 10l4.8-1.7zM18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z",
  campaigns: "M3.5 11.5L20 5l-6.5 16-2.7-7.3z",
  settings:
    "M12 15.2a3.2 3.2 0 100-6.4 3.2 3.2 0 000 6.4zM19.2 15a1.6 1.6 0 00.3 1.8l.1.1-2.1 2.1-.1-.1a1.6 1.6 0 00-1.8-.3 1.6 1.6 0 00-1 1.5v.2h-3v-.2a1.6 1.6 0 00-1-1.5 1.6 1.6 0 00-1.8.3l-.1.1-2.1-2.1.1-.1a1.6 1.6 0 00.3-1.8 1.6 1.6 0 00-1.5-1H4v-3h.2a1.6 1.6 0 001.5-1 1.6 1.6 0 00-.3-1.8l-.1-.1 2.1-2.1.1.1a1.6 1.6 0 001.8.3 1.6 1.6 0 001-1.5V4h3v.2a1.6 1.6 0 001 1.5 1.6 1.6 0 001.8-.3l.1-.1 2.1 2.1-.1.1a1.6 1.6 0 00-.3 1.8 1.6 1.6 0 001.5 1h.2v3h-.2a1.6 1.6 0 00-1.5 1z",
  logout: "M9 5H5v14h4M15 8l4 4-4 4M19 12H9",
};

export function Icon({ name, size = 18 }) {
  return html`
    <svg
      width=${size}
      height=${size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.8"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d=${iconPaths[name] || iconPaths.dashboard}></path>
    </svg>
  `;
}

export function Button({
  variant = "primary",
  size,
  type = "button",
  className = "",
  onClick,
  disabled = false,
  title,
  ariaLabel,
  children,
}) {
  const classes = ["btn", `btn-${variant}`, size ? `btn-${size}` : "", className]
    .filter(Boolean)
    .join(" ");
  return html`
    <button
      type=${type}
      class=${classes}
      onClick=${onClick}
      disabled=${disabled}
      title=${title}
      aria-label=${ariaLabel}
    >
      ${children}
    </button>
  `;
}

export function Spinner({ size = "" }) {
  return html`<span class=${`spinner ${size ? "spinner-lg" : ""}`} role="status" aria-label="Loading"></span>`;
}

export function LoadingRow({ label = "Loading…" }) {
  return html`<div class="loading-row"><${Spinner} /> ${label}</div>`;
}

export function ErrorBox({ message }) {
  if (!message) return null;
  return html`<p class="form-error" role="alert">${message}</p>`;
}

export function EmptyState({ icon = "mailbox", title, children }) {
  return html`
    <div class="empty">
      <div class="empty-icon"><${Icon} name=${icon} size=${22} /></div>
      <div class="empty-title">${title}</div>
      <p>${children}</p>
    </div>
  `;
}

export function ProgressBar({ value, max, label }) {
  const safeMax = Math.max(1, max || 1);
  const pct = Math.min(100, Math.max(0, Math.round((value / safeMax) * 100)));
  return html`
    <div>
      <div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax=${safeMax} aria-valuenow=${value}>
        <div class="progress-bar" style=${{ width: `${pct}%` }}></div>
      </div>
      ${label &&
      html`<div class="progress-meta"><span>${label}</span><span>${pct}%</span></div>`}
    </div>
  `;
}

export function Pagination({ page, pageSize, total, onPage, onPageSize, pageSizes = [25, 50, 100] }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const start = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const end = Math.min(total, page * pageSize);
  return html`
    <div class="pagination">
      <span>${total === 0 ? "0 results" : `${start}–${end} of ${total}`}</span>
      <div class="pagination-actions">
        <label class="tiny muted" for="page-size">Rows</label>
        <select
          id="page-size"
          value=${String(pageSize)}
          onChange=${(event) => onPageSize(Number(event.target.value))}
        >
          ${pageSizes.map((size) => html`<option value=${String(size)}>${size}</option>`)}
        </select>
        <${Button} variant="secondary" size="sm" disabled=${page <= 1} onClick=${() => onPage(page - 1)}>
          Prev
        </${Button}>
        <span class="tiny muted">Page ${page} / ${pages}</span>
        <${Button} variant="secondary" size="sm" disabled=${page >= pages} onClick=${() => onPage(page + 1)}>
          Next
        </${Button}>
      </div>
    </div>
  `;
}

export function Modal({ title, children, onClose, footer }) {
  const ref = useRef(null);
  useEffect(() => {
    const onKey = (event) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    const previous = document.activeElement;
    if (ref.current) ref.current.focus();
    return () => {
      document.removeEventListener("keydown", onKey);
      if (previous && previous.focus) previous.focus();
    };
  }, [onClose]);

  return html`
    <div
      class="modal-backdrop"
      onMouseDown=${(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div class="modal" role="dialog" aria-modal="true" aria-label=${title} tabindex="-1" ref=${ref}>
        <div class="modal-head">
          <h2 class="modal-title">${title}</h2>
          <button class="icon-btn" type="button" aria-label="Close" onClick=${onClose}>×</button>
        </div>
        <div class="modal-body">${children}</div>
        ${footer && html`<div class="modal-actions">${footer}</div>`}
      </div>
    </div>
  `;
}

let toastSeq = 0;

export function toast(message, type = "info") {
  toastSeq += 1;
  window.dispatchEvent(
    new CustomEvent("mc:toast", { detail: { id: toastSeq, message, type } }),
  );
}

export function Toaster() {
  const [items, setItems] = useState([]);
  useEffect(() => {
    const onToast = (event) => {
      const item = event.detail;
      setItems((current) => [...current, item]);
      window.setTimeout(() => {
        setItems((current) => current.filter((entry) => entry.id !== item.id));
      }, 4500);
    };
    window.addEventListener("mc:toast", onToast);
    return () => window.removeEventListener("mc:toast", onToast);
  }, []);

  return html`
    <div class="toast-stack" aria-live="polite" aria-atomic="false">
      ${items.map(
        (item) => html`<div key=${item.id} class=${`toast ${item.type}`}>${item.message}</div>`,
      )}
    </div>
  `;
}
