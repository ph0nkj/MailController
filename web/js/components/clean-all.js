import { useState, useEffect, useRef } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { api } from "../api.js";
import { Button, ErrorBox, Modal, ProgressBar, Spinner, toast } from "./ui.js";

const MODES = [
  {
    id: "mark_read",
    title: "Mark all as read",
    note: "Removes the unread flag from every unread Inbox message.",
  },
  {
    id: "archive",
    title: "Archive all",
    note: "Removes every message from the Inbox. Messages stay in All Mail.",
  },
  {
    id: "trash",
    title: "Move all to Trash",
    note: "Moves every Inbox message to Trash. Google permanently deletes Trash after 30 days.",
  },
];

const MODE_LABELS = {
  mark_read: "Mark as read",
  archive: "Archive",
  trash: "Move to Trash",
};

function modeUnit(mode) {
  return mode === "mark_read" ? "unread messages" : "Inbox messages";
}

export function CleanAll({ disabled = false, onComplete }) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState("mark_read");
  const [preview, setPreview] = useState(null);
  const [previewing, setPreviewing] = useState(false);
  const [running, setRunning] = useState(false);
  const [job, setJob] = useState(null);
  const [error, setError] = useState("");
  const timer = useRef(null);

  useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  function openModal() {
    setOpen(true);
    setMode("mark_read");
    setPreview(null);
    setPreviewing(false);
    setRunning(false);
    setJob(null);
    setError("");
  }

  function closeModal() {
    setOpen(false);
  }

  function chooseMode(next) {
    setMode(next);
    setPreview(null);
    setJob(null);
    setError("");
  }

  async function loadPreview() {
    setPreviewing(true);
    setError("");
    try {
      const result = await api.cleanPreview(mode);
      setPreview(result);
    } catch (err) {
      setError(err.message || "Could not count messages");
    } finally {
      setPreviewing(false);
    }
  }

  function poll(jobId) {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(async () => {
      try {
        const current = await api.job(jobId);
        setJob(current);
        if (current.status === "done") {
          setRunning(false);
          toast(
            `${MODE_LABELS[mode] || "Cleanup"} finished: ${current.done} message${current.done === 1 ? "" : "s"}`,
            "success",
          );
          if (onComplete) onComplete();
          return;
        }
        if (current.status === "failed") {
          setRunning(false);
          setError(current.error || "Cleanup failed");
          toast("Cleanup failed", "error");
          return;
        }
        poll(jobId);
      } catch (err) {
        setRunning(false);
        setError(err.message || "Lost track of the cleanup job");
      }
    }, 1000);
  }

  async function execute() {
    if (!preview || running) return;
    setRunning(true);
    setError("");
    setJob(null);
    try {
      const started = await api.cleanExecute(mode, preview.token);
      setJob({ id: started.job_id, status: "running", total: started.count, done: 0 });
      poll(started.job_id);
    } catch (err) {
      setRunning(false);
      setError(err.message || "Could not start cleanup");
      if (err.code === "confirm_expired") setPreview(null);
    }
  }

  const finished = job && (job.status === "done" || job.status === "failed");
  const countKnown = preview && typeof preview.count === "number";
  const nothingToDo = countKnown && preview.count === 0;

  let body;
  if (job) {
    body = html`
      <p>
        ${job.status === "done"
          ? html`<strong>${MODE_LABELS[mode]}</strong> completed.`
          : job.status === "failed"
            ? html`<strong>${MODE_LABELS[mode]}</strong> did not finish.`
            : html`<strong>${MODE_LABELS[mode]}</strong> is running in the background.`}
      </p>
      <${ProgressBar}
        value=${job.done || 0}
        max=${job.total || job.done || 1}
        label=${`${job.done || 0} of ${job.total || "?"} messages`}
      />
      ${job.error && html`<p class="form-error" role="alert">${job.error}</p>`}
      ${job.status === "running" &&
      html`<p class="hint" style=${{ marginTop: "12px" }}>
        You can close this dialog; cleanup continues on the server.
      </p>`}
    `;
  } else if (preview) {
    body = html`
      <p>This will affect:</p>
      <div class="clean-count">
        <strong>${preview.count.toLocaleString()}</strong> ${modeUnit(mode)}
      </div>
      ${nothingToDo
        ? html`<p class="form-success">Nothing to clean up right now.</p>`
        : html`<p class="hint">
            The confirmation is valid for 5 minutes. Nothing has been changed yet.
          </p>`}
    `;
  } else {
    body = html`
      <p>
        Choose what Clean All should do with your Inbox. You will see a count before anything
        changes.
      </p>
      <div class="clean-modes">
        ${MODES.map(
          (option) => html`
            <label class="clean-mode" key=${option.id}>
              <input
                type="radio"
                name="clean-mode"
                value=${option.id}
                checked=${mode === option.id}
                onChange=${() => chooseMode(option.id)}
              />
              <span>
                <span class="clean-mode-title">${option.title}</span>
                <span class="clean-mode-note">${option.note}</span>
              </span>
            </label>
          `,
        )}
      </div>
    `;
  }

  let footer;
  if (job) {
    footer = html`
      <${Button} variant="secondary" onClick=${closeModal}>Close</${Button}>
      ${finished &&
      html`<${Button}
        variant="primary"
        onClick=${() => {
          setJob(null);
          setPreview(null);
        }}
      >Run again</${Button}>`}
    `;
  } else if (preview) {
    footer = html`
      <${Button} variant="secondary" onClick=${() => setPreview(null)} disabled=${running}>Back</${Button}>
      <${Button}
        variant=${mode === "trash" ? "danger" : "primary"}
        onClick=${execute}
        disabled=${running || nothingToDo}
      >
        ${running ? "Starting…" : nothingToDo ? "Nothing to do" : `Confirm ${MODE_LABELS[mode]}`}
      </${Button}>
    `;
  } else {
    footer = html`
      <${Button} variant="secondary" onClick=${closeModal}>Cancel</${Button}>
      <${Button} variant="primary" onClick=${loadPreview} disabled=${previewing}>
        ${previewing ? html`<${Spinner} /> Counting…` : "Preview count"}
      </${Button}>
    `;
  }

  return html`
    <${Button} variant="danger-outline" onClick=${openModal} disabled=${disabled}>
      Clean All
    </${Button}>
    ${open &&
    html`
      <${Modal} title="Clean All" onClose=${closeModal} footer=${footer}>
        <${ErrorBox} message=${error} />
        ${body}
      </${Modal}>
    `}
  `;
}
