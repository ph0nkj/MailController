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
  ProgressBar,
  Spinner,
  toast,
} from "../components/ui.js";

const PROMPT_LIMIT = 2000;

const EXAMPLES = [
  "Archive all read newsletters older than a year",
  "Mark every unread notification as read",
  "Move promotions older than 30 days to Trash",
];

const ACTION_LABELS = {
  mark_read: "Mark read",
  mark_unread: "Mark unread",
  archive: "Archive",
  trash: "Move to Trash",
};

const ACTION_BADGES = {
  mark_read: "badge-green",
  mark_unread: "badge-blue",
  archive: "badge-blue",
  trash: "badge-amber",
};

const STATUS_META = {
  planning: { label: "Planning", badge: "badge-gray" },
  planned: { label: "Planned", badge: "badge-amber" },
  running: { label: "Running", badge: "badge-blue" },
  done: { label: "Done", badge: "badge-green" },
  failed: { label: "Failed", badge: "badge-red" },
  cancelled: { label: "Cancelled", badge: "badge-gray" },
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

function ActionBadge({ action }) {
  return html`
    <span class=${`badge ${ACTION_BADGES[action] || "badge-gray"}`}>
      ${ACTION_LABELS[action] || action}
    </span>
  `;
}

function agentErrorMessage(error) {
  if (!error) return "";
  switch (error.code) {
    case "llm_not_configured":
      return "Connect an AI provider in Settings before asking the assistant to plan.";
    case "google_not_connected":
      return "Connect a Google account in Settings first.";
    case "google_not_configured":
      return "Save your Google OAuth client credentials in Settings first.";
    case "confirm_expired":
      return "This confirmation expired. Create a new plan to continue.";
    case "agent_busy":
      return "Another agent run is executing. Wait for it to finish, then create a new plan.";
    case "run_not_ready":
      return "This plan is no longer ready to execute. Create a new plan.";
    default:
      return error.message || "The request failed.";
  }
}

function planMessage(run) {
  if (!run) return "";
  switch (run.status) {
    case "done":
      return `${(run.result && run.result.affected ? run.result.affected : 0).toLocaleString()} messages affected`;
    case "failed":
      return (run.result && run.result.error) || "Execution failed";
    case "running":
      return "Executing now";
    case "planned":
      return run.plan && run.plan.total
        ? `${run.plan.total.toLocaleString()} messages planned`
        : "Plan ready";
    case "planning":
      return "Planning";
    case "cancelled":
      return "Cancelled";
    default:
      return "";
  }
}

function PlanTable({ plan }) {
  if (!plan || !Array.isArray(plan.steps) || plan.steps.length === 0) {
    return html`<p class="hint">This plan has no steps.</p>`;
  }
  return html`
    <div class="table-wrap agent-plan-table">
      <table class="table">
        <thead>
          <tr>
            <th>Action</th>
            <th>Gmail query</th>
            <th>Matches</th>
            <th>Reason</th>
          </tr>
        </thead>
        <tbody>
          ${plan.steps.map(
            (step, index) => html`
              <tr key=${index}>
                <td data-label="Action"><${ActionBadge} action=${step.action} /></td>
                <td data-label="Gmail query"><span class="mono agent-query">${step.query}</span></td>
                <td class="agent-count" data-label="Matches">${Number(step.count || 0).toLocaleString()}</td>
                <td class="agent-reason" data-label="Reason">${step.reason || "—"}</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
    </div>
  `;
}

function ResultList({ result }) {
  if (!result || !Array.isArray(result.steps) || result.steps.length === 0) return null;
  return html`
    <ul class="agent-result-list">
      ${result.steps.map(
        (step, index) => html`
          <li key=${index}>
            <span>${ACTION_LABELS[step.action] || step.action}</span>
            <span class="tiny muted">${Number(step.affected || 0).toLocaleString()} affected</span>
          </li>
        `,
      )}
    </ul>
  `;
}

export function Assistant() {
  const [prompt, setPrompt] = useState("");
  const [preview, setPreview] = useState(null);
  const [planning, setPlanning] = useState(false);
  const [planError, setPlanError] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [active, setActive] = useState(null);
  const [runs, setRuns] = useState([]);
  const [runsLoading, setRunsLoading] = useState(true);
  const [runsError, setRunsError] = useState("");
  const [runDetail, setRunDetail] = useState(null);
  const pollTimer = useRef(null);
  const runsSeq = useRef(0);

  function loadRuns({ quiet = false } = {}) {
    const seq = runsSeq.current + 1;
    runsSeq.current = seq;
    if (!quiet) setRunsLoading(true);
    setRunsError("");
    return api
      .agentRuns(10)
      .then((next) => {
        if (runsSeq.current !== seq) return;
        setRuns(next && Array.isArray(next.items) ? next.items : []);
      })
      .catch((err) => {
        if (runsSeq.current !== seq) return;
        if (!quiet) setRunsError(err.message || "Could not load recent runs");
      })
      .finally(() => {
        if (runsSeq.current === seq && !quiet) setRunsLoading(false);
      });
  }

  useEffect(() => {
    loadRuns();
    return () => {
      runsSeq.current += 1;
      if (pollTimer.current) window.clearTimeout(pollTimer.current);
    };
  }, []);

  function pollJob(jobId, runId) {
    if (pollTimer.current) window.clearTimeout(pollTimer.current);
    pollTimer.current = window.setTimeout(async () => {
      try {
        const job = await api.job(jobId);
        setActive((current) => (current && current.jobId === jobId ? { ...current, job } : current));
        if (job.status === "done" || job.status === "failed") {
          let run = null;
          try {
            run = await api.agentRun(runId);
          } catch {
            run = null;
          }
          setActive((current) =>
            current && current.jobId === jobId ? { ...current, run } : current,
          );
          loadRuns({ quiet: true });
          if (job.status === "done") {
            toast(`Plan applied to ${job.done.toLocaleString()} messages`, "success");
          } else {
            toast("Plan execution failed", "error");
          }
          return;
        }
        pollJob(jobId, runId);
      } catch (err) {
        setActive((current) =>
          current && current.jobId === jobId
            ? { ...current, error: err.message || "Lost track of the execution job" }
            : current,
        );
      }
    }, 1000);
  }

  async function createPlan(event) {
    event.preventDefault();
    const request = prompt.trim();
    if (!request || planning) return;
    setPlanning(true);
    setPlanError("");
    setPreview(null);
    try {
      const result = await api.agentPlan(request);
      setPreview(result);
    } catch (err) {
      setPlanError(agentErrorMessage(err));
    } finally {
      setPlanning(false);
    }
  }

  async function executePlan() {
    if (!preview || executing) return;
    setExecuting(true);
    setPlanError("");
    try {
      const started = await api.agentExecute(preview.id, preview.confirm_token);
      const total = preview.plan && preview.plan.total ? preview.plan.total : 0;
      setConfirmOpen(false);
      setPreview(null);
      setActive({
        runId: started.run_id,
        jobId: started.job_id,
        job: { status: "running", total, done: 0 },
        run: null,
        error: "",
      });
      pollJob(started.job_id, started.run_id);
    } catch (err) {
      setConfirmOpen(false);
      setPreview(null);
      setPlanError(agentErrorMessage(err));
      toast("Could not start the plan", "error");
    } finally {
      setExecuting(false);
    }
  }

  function useExample(example) {
    setPrompt(example);
    setPlanError("");
  }

  function discardPlan() {
    setPreview(null);
    setPlanError("");
  }

  function startAnother() {
    if (pollTimer.current) window.clearTimeout(pollTimer.current);
    pollTimer.current = null;
    setActive(null);
    setPrompt("");
    setPlanError("");
  }

  const hasTrash = Boolean(
    preview && preview.plan && preview.plan.steps.some((step) => step.action === "trash"),
  );
  const planTotal = preview && preview.plan && preview.plan.total ? preview.plan.total : 0;

  const job = active && active.job;
  const run = active && active.run;
  const jobDone = Boolean(job && (job.status === "done" || job.status === "failed"));
  const activeStatus = run
    ? run.status
    : job && job.status === "failed"
      ? "failed"
      : job && job.status === "done"
        ? "done"
        : "running";

  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Assistant</h1>
          <p class="page-sub">
            Describe a mailbox cleanup in plain language. Review the exact plan before anything changes.
          </p>
        </div>
        <div class="page-actions">
          <${Link} href="/settings" className="btn btn-secondary btn-sm">AI settings</${Link}>
        </div>
      </div>

      <div class="agent-layout">
        <div class="stack">
          <section class="card agent-compose">
            <div class="row-between">
              <div>
                <h2 class="card-title">What should change?</h2>
                <p class="card-sub">
                  The planner only sees your request. It does not read email bodies and cannot send mail.
                </p>
              </div>
              <span class="badge badge-blue">Preview first</span>
            </div>

            <form onSubmit=${createPlan}>
              <div class="field">
                <label class="label" for="agent-prompt">Request</label>
                <textarea
                  id="agent-prompt"
                  class="textarea agent-prompt"
                  maxlength=${PROMPT_LIMIT}
                  placeholder="e.g. Archive all read newsletters older than a year"
                  value=${prompt}
                  disabled=${planning}
                  onInput=${(event) => setPrompt(event.target.value)}
                ></textarea>
                <div class="row-between agent-prompt-meta">
                  <span class="hint">Only Gmail search actions are supported.</span>
                  <span class="tiny muted">${prompt.length} / ${PROMPT_LIMIT}</span>
                </div>
              </div>

              <div class="agent-examples" aria-label="Example requests">
                ${EXAMPLES.map(
                  (example) => html`
                    <button
                      key=${example}
                      type="button"
                      class="agent-example"
                      onClick=${() => useExample(example)}
                      disabled=${planning}
                    >
                      ${example}
                    </button>
                  `,
                )}
              </div>

              <${ErrorBox} message=${planError} />

              <div class="row-between agent-compose-actions">
                <span class="hint">
                  Planning uses your configured provider.${" "}
                  <${Link} href="/settings" className="link-button">Open AI settings</${Link}>
                </span>
                <${Button} type="submit" variant="primary" disabled=${planning || !prompt.trim()}>
                  ${planning ? html`<${Spinner} /> Planning…` : "Preview plan"}
                </${Button}>
              </div>
            </form>
          </section>

          ${preview &&
          html`
            <section class="card agent-plan">
              <div class="row-between">
                <div>
                  <h2 class="card-title">Plan preview</h2>
                  <p class="card-sub">${preview.plan.summary || "Review the actions below."}</p>
                </div>
                <span class="badge badge-amber">Nothing changed yet</span>
              </div>

              <${PlanTable} plan=${preview.plan} />

              <div class="agent-plan-total">
                <div>
                  <span class="agent-total-number">${planTotal.toLocaleString()}</span>
                  <span class="muted"> messages will be affected</span>
                </div>
                <span class="tiny muted">
                  Confirmation valid until ${formatDate(preview.expires_at)}
                </span>
              </div>

              ${hasTrash &&
              html`
                <p class="agent-warning">
                  This plan moves messages to Trash. They stay recoverable for 30 days; permanent
                  deletion is never performed by MailController.
                </p>
              `}

              <div class="row agent-plan-actions">
                <${Button} variant="secondary" onClick=${discardPlan} disabled=${executing}>
                  Discard
                </${Button}>
                <${Button}
                  variant=${hasTrash ? "danger" : "primary"}
                  onClick=${() => setConfirmOpen(true)}
                  disabled=${executing}
                >
                  Review and apply
                </${Button}>
              </div>
            </section>
          `}

          ${active &&
          html`
            <section class="card agent-progress" aria-live="polite">
              <div class="row-between">
                <div>
                  <h2 class="card-title">Execution</h2>
                  <p class="card-sub">
                    ${jobDone
                      ? "The run has finished. Results are stored in the audit log."
                      : "MailController is applying the approved plan in the background."}
                  </p>
                </div>
                <${StatusBadge} status=${activeStatus} />
              </div>

              <${ProgressBar}
                value=${job && job.done ? job.done : 0}
                max=${job && job.total ? job.total : 1}
                label=${`${job && job.done ? job.done.toLocaleString() : 0} of ${job && job.total ? job.total.toLocaleString() : "?"} messages processed`}
              />

              ${active.error && html`<${ErrorBox} message=${active.error} />`}
              ${job && job.error && html`<${ErrorBox} message=${job.error} />`}

              ${run &&
              run.status === "done" &&
              html`
                <div class="agent-result">
                  <p class="form-success">
                    Applied the plan to ${Number(run.result.affected || 0).toLocaleString()} messages.
                  </p>
                  <${ResultList} result=${run.result} />
                </div>
              `}
              ${run &&
              run.status === "failed" &&
              html`<${ErrorBox} message=${run.result.error || "The run failed before finishing."} />`}

              ${!jobDone &&
              html`
                <p class="hint agent-progress-hint">
                  You can leave this page; execution continues on the server.
                </p>
              `}

              ${(jobDone || active.error) &&
              html`
                <div class="row agent-plan-actions">
                  <${Button} variant="primary" onClick=${startAnother}>Start another</${Button}>
                  <${Button} variant="secondary" onClick=${() => loadRuns()}>Refresh history</${Button}>
                </div>
              `}
            </section>
          `}
        </div>

        <div class="stack">
          <section class="card">
            <div class="row-between">
              <div>
                <h2 class="card-title">Recent runs</h2>
                <p class="card-sub">Plans and outcomes from this installation.</p>
              </div>
              <${Button} variant="ghost" size="sm" onClick=${() => loadRuns()} disabled=${runsLoading}>
                ${runsLoading ? html`<${Spinner} />` : "Refresh"}
              </${Button}>
            </div>

            ${runsLoading
              ? html`<${LoadingRow} label="Loading runs…" />`
              : runsError
                ? html`<${ErrorBox} message=${runsError} />`
                : runs.length === 0
                  ? html`
                      <${EmptyState} icon="assistant" title="No runs yet">
                        Create a plan to see it here with its status and result.
                      </${EmptyState}>
                    `
                  : html`
                      <div class="agent-run-list">
                        ${runs.map(
                          (item) => html`
                            <button
                              key=${item.id}
                              type="button"
                              class="agent-run"
                              onClick=${() => setRunDetail(item)}
                            >
                              <span class="agent-run-top">
                                <${StatusBadge} status=${item.status} />
                                <span class="tiny muted">${formatDate(item.created_at)}</span>
                              </span>
                              <span class="agent-run-prompt">${item.prompt}</span>
                              <span class="agent-run-meta">${planMessage(item)}</span>
                            </button>
                          `,
                        )}
                      </div>
                    `}
          </section>

          <section class="card">
            <h2 class="card-title">Guardrails</h2>
            <ul class="ai-guardrails">
              <li>Plans are counted first and shown for review; nothing changes until you confirm.</li>
              <li>Only mark read/unread, archive and Trash actions are allowed. Permanent deletion is never used.</li>
              <li>The planner never receives email bodies or message content; it only sees your request.</li>
              <li>Each run is bounded by the server's message and step limits and recorded in the audit log.</li>
            </ul>
          </section>
        </div>
      </div>

      ${confirmOpen &&
      preview &&
      html`
        <${Modal}
          title="Apply this plan?"
          onClose=${() => {
            if (!executing) setConfirmOpen(false);
          }}
          footer=${html`
            <${Button}
              variant="secondary"
              onClick=${() => setConfirmOpen(false)}
              disabled=${executing}
            >
              Cancel
            </${Button}>
            <${Button}
              variant=${hasTrash ? "danger" : "primary"}
              onClick=${executePlan}
              disabled=${executing}
            >
              ${executing ? "Starting…" : `Apply to ${planTotal.toLocaleString()} messages`}
            </${Button}>
          `}
        >
          <p>
            MailController will apply <strong>${preview.plan.steps.length}</strong>
            action${preview.plan.steps.length === 1 ? "" : "s"} to
            <strong>${planTotal.toLocaleString()}</strong>
            message${planTotal === 1 ? "" : "s"}.
          </p>
          <ul class="modal-list">
            ${preview.plan.steps.map(
              (step, index) => html`
                <li key=${index}>
                  <strong>${ACTION_LABELS[step.action] || step.action}</strong>:
                  <span class="mono agent-query">${step.query}</span>
                  (${Number(step.count || 0).toLocaleString()} matches)
                </li>
              `,
            )}
          </ul>
          ${hasTrash &&
          html`
            <p class="hint">
              Trash is recoverable for 30 days. No permanent deletion is performed.
            </p>
          `}
          <p class="hint">This confirmation can be used once and expires in a few minutes.</p>
        </${Modal}>
      `}

      ${runDetail &&
      html`
        <${Modal}
          title="Run details"
          className="modal-wide"
          onClose=${() => setRunDetail(null)}
          footer=${html`<${Button} variant="secondary" onClick=${() => setRunDetail(null)}>Close</${Button}>`}
        >
          <div class="row-between agent-detail-head">
            <${StatusBadge} status=${runDetail.status} />
            <span class="tiny muted">${formatDate(runDetail.created_at)}</span>
          </div>
          <p class="agent-detail-prompt">${runDetail.prompt}</p>
          ${runDetail.plan && runDetail.plan.summary
            ? html`<p class="card-sub">${runDetail.plan.summary}</p>`
            : null}
          <${PlanTable} plan=${runDetail.plan} />
          ${runDetail.status === "done" &&
          html`
            <p class="form-success agent-detail-result">
              ${Number(runDetail.result.affected || 0).toLocaleString()} messages affected.
            </p>
            <${ResultList} result=${runDetail.result} />
          `}
          ${runDetail.status === "failed" &&
          html`<${ErrorBox} message=${runDetail.result.error || "The run failed."} />`}
        </${Modal}>
      `}
    </div>
  `;
}
