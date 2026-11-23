import { html } from "../html.js";
import { Link } from "../router.js";

export function Dashboard() {
  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Dashboard</h1>
          <p class="page-sub">Mailbox cleanup, bulk mail and AI actions in one place.</p>
        </div>
      </div>
      <div class="stack">
        <section class="card">
          <h2 class="card-title">Get started</h2>
          <p class="card-sub">
            Connect your Google account and configure the AI provider to unlock every feature.
          </p>
          <div class="row">
            <${Link} href="/settings" className="btn btn-primary">Open settings</${Link}>
            <${Link} href="/mailbox" className="btn btn-secondary">Browse mailbox</${Link}>
          </div>
        </section>
        <div class="stat-grid">
          <div class="stat">
            <div class="stat-label">Gmail account</div>
            <div class="stat-value stat-value-sm">Not connected</div>
            <div class="stat-note">Finish setup in Settings</div>
          </div>
          <div class="stat">
            <div class="stat-label">Mailbox</div>
            <div class="stat-value stat-value-sm">Ready</div>
            <div class="stat-note">Search, filter and clean</div>
          </div>
          <div class="stat">
            <div class="stat-label">Campaigns</div>
            <div class="stat-value stat-value-sm">0 sent</div>
            <div class="stat-note">Throttled bulk sending</div>
          </div>
        </div>
      </div>
    </div>
  `;
}
