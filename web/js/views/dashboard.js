import { useState, useEffect } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { Link } from "../router.js";
import { api } from "../api.js";
import { CleanAll } from "../components/clean-all.js";
import { ErrorBox, LoadingRow } from "../components/ui.js";

function Stat({ label, value, note, tone }) {
  return html`
    <div class="stat">
      <div class="stat-label">${label}</div>
      <div class=${`stat-value ${tone === "small" ? "stat-value-sm" : ""}`}>${value}</div>
      <div class="stat-note">${note}</div>
    </div>
  `;
}

export function Dashboard() {
  const [status, setStatus] = useState(null);
  const [stats, setStats] = useState(null);
  const [campaigns, setCampaigns] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [statsError, setStatsError] = useState("");

  async function load() {
    setError("");
    setStatsError("");
    const [statusResult, statsResult, campaignsResult] = await Promise.allSettled([
      api.googleStatus(),
      api.mailStats(),
      api.campaigns(),
    ]);
    if (statusResult.status === "fulfilled") {
      setStatus(statusResult.value);
    } else {
      setError(statusResult.reason?.message || "Could not load Google status");
    }
    if (statsResult.status === "fulfilled") {
      setStats(statsResult.value);
    } else {
      setStats(null);
      setStatsError(statsResult.reason?.message || "Could not load mailbox statistics");
    }
    if (campaignsResult.status === "fulfilled") {
      setCampaigns(campaignsResult.value);
    } else {
      setCampaigns(null);
    }
    setLoading(false);
  }

  useEffect(() => {
    load();
  }, []);

  const connected = Boolean(status && status.connected);
  const number = (value) => (typeof value === "number" ? value.toLocaleString() : "—");
  const campaignItems = campaigns && Array.isArray(campaigns.items) ? campaigns.items : [];
  const sentTotal = campaignItems.reduce(
    (sum, campaign) => sum + ((campaign.stats && campaign.stats.sent) || 0),
    0,
  );
  const runningCampaigns = campaignItems.filter((campaign) => campaign.status === "running").length;
  const campaignNote = campaigns
    ? `${sentTotal.toLocaleString()} emails sent${runningCampaigns ? ` · ${runningCampaigns} running` : ""}`
    : "Open campaigns to see progress";

  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Dashboard</h1>
          <p class="page-sub">Mailbox cleanup, bulk mail and AI actions in one place.</p>
        </div>
        <div class="page-actions">
          <${CleanAll} disabled=${!connected || loading} onComplete=${load} />
        </div>
      </div>

      ${error && html`<${ErrorBox} message=${error} />`}

      ${loading
        ? html`<div class="card"><${LoadingRow} label="Loading dashboard…" /></div>`
        : html`
            <div class="stack">
              <div class="stat-grid">
                <${Stat}
                  label="Gmail account"
                  value=${connected ? status.email : "Not connected"}
                  tone="small"
                  note=${connected ? "Connected" : "Finish setup in Settings"}
                />
                <${Stat}
                  label="Inbox"
                  value=${number(stats ? stats.inbox_total : null)}
                  note=${connected ? "Messages in Inbox" : "Connect Google to load"}
                />
                <${Stat}
                  label="Unread"
                  value=${number(stats ? stats.inbox_unread : null)}
                  note=${connected ? "Unread messages in Inbox" : "Connect Google to load"}
                />
                <${Stat} label="Campaigns" value=${number(campaignItems.length)} tone="small" note=${campaignNote} />
              </div>

              ${!connected &&
              html`
                <section class="card">
                  <h2 class="card-title">Connect your Gmail account</h2>
                  <p class="card-sub">
                    MailController needs an OAuth client and a connected account before it can list
                    or clean mail.
                  </p>
                  <div class="row">
                    <${Link} href="/settings" className="btn btn-primary">Open settings</${Link}>
                  </div>
                </section>
              `}

              ${connected && statsError &&
              html`<p class="hint">Mailbox statistics: ${statsError}</p>`}

              <section class="card">
                <h2 class="card-title">Quick actions</h2>
                <p class="card-sub">Jump straight into the most common tasks.</p>
                <div class="row">
                  <${Link} href="/mailbox" className="btn btn-secondary">Browse mailbox</${Link}>
                  <${Link} href="/campaigns" className="btn btn-secondary">Campaigns</${Link}>
                  <${Link} href="/settings" className="btn btn-ghost">Account settings</${Link}>
                </div>
              </section>
            </div>
          `}
    </div>
  `;
}
