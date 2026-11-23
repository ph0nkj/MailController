import { useState, useEffect } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { api } from "../api.js";
import {
  Button,
  ErrorBox,
  LoadingRow,
  Modal,
  Spinner,
  toast,
} from "../components/ui.js";

export function Settings() {
  const [status, setStatus] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);

  async function loadStatus() {
    setLoadError("");
    try {
      const next = await api.googleStatus();
      setStatus(next);
      setClientId(next.client_id || "");
      setClientSecret("");
    } catch (err) {
      setLoadError(err.message || "Could not load Google status");
    }
  }

  useEffect(() => {
    loadStatus();
    const params = new URLSearchParams(window.location.search);
    if (params.get("google") === "connected") {
      toast("Google account connected", "success");
    } else if (params.get("google") === "error") {
      toast("Google connection failed", "error");
    }
    if (params.has("google")) {
      params.delete("google");
      const query = params.toString();
      window.history.replaceState({}, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
    }
  }, []);

  async function saveClient(event) {
    event.preventDefault();
    if (saving) return;
    setFormError("");
    setSaving(true);
    try {
      const next = await api.saveGoogleClient(clientId.trim(), clientSecret.trim());
      setStatus(next);
      setClientSecret("");
      toast("OAuth client saved", "success");
    } catch (err) {
      setFormError(err.message || "Could not save credentials");
    } finally {
      setSaving(false);
    }
  }

  async function disconnect() {
    setDisconnecting(true);
    setFormError("");
    try {
      await api.disconnectGoogle();
      setConfirmDisconnect(false);
      await loadStatus();
      toast("Google account disconnected", "success");
    } catch (err) {
      setFormError(err.message || "Could not disconnect account");
    } finally {
      setDisconnecting(false);
      setConfirmDisconnect(false);
    }
  }

  function connect() {
    window.location.href = "/api/v1/google/connect";
  }

  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Settings</h1>
          <p class="page-sub">Google account, AI provider and sending limits.</p>
        </div>
      </div>

      <div class="stack">
        <section class="card">
          <div class="row-between">
            <div>
              <h2 class="card-title">Google account</h2>
              <p class="card-sub">Connect the Gmail account MailController will manage.</p>
            </div>
            ${status &&
            html`
              <span class=${`badge ${status.connected ? "badge-green" : "badge-gray"}`}>
                ${status.connected ? "Connected" : "Not connected"}
              </span>
            `}
          </div>

          <${ErrorBox} message=${loadError} />
          ${!status
            ? html`<${LoadingRow} label="Loading Google status…" />`
            : html`
                ${status.connected &&
                html`
                  <div class="row-between card card-flat">
                    <div>
                      <div class="label">Connected mailbox</div>
                      <div class="mono">${status.email}</div>
                    </div>
                    <${Button}
                      variant="danger-outline"
                      size="sm"
                      onClick=${() => setConfirmDisconnect(true)}
                    >
                      Disconnect
                    </${Button}>
                  </div>
                `}

                <p class="hint">
                  Create an OAuth client in Google Cloud, enable the Gmail API and add this
                  authorized redirect URI:
                </p>
                <div class="code-box mono">${status.redirect_url}</div>

                <form onSubmit=${saveClient} class="mt-4">
                  <${ErrorBox} message=${formError} />
                  <div class="form-grid">
                    <div class="field">
                      <label class="label" for="google-client-id">Client ID</label>
                      <input
                        id="google-client-id"
                        class="input"
                        value=${clientId}
                        onInput=${(event) => setClientId(event.target.value)}
                        placeholder="1234567890-abc.apps.googleusercontent.com"
                        autocomplete="off"
                        required
                      />
                    </div>
                    <div class="field">
                      <label class="label" for="google-client-secret">Client secret</label>
                      <input
                        id="google-client-secret"
                        class="input"
                        type="password"
                        value=${clientSecret}
                        onInput=${(event) => setClientSecret(event.target.value)}
                        placeholder=${status.has_client_secret ? "Saved — enter a new secret to replace" : "GOCSPX-…"}
                        autocomplete="new-password"
                        required
                      />
                    </div>
                  </div>
                  <div class="row">
                    <${Button} type="submit" variant="primary" disabled=${saving}>
                      ${saving ? html`<${Spinner} /> Saving…` : "Save OAuth client"}
                    </${Button}>
                    <${Button}
                      variant="secondary"
                      onClick=${connect}
                      disabled=${!status.configured}
                      title=${status.configured ? "Start Google sign-in" : "Save the OAuth client first"}
                    >
                      Connect Google
                    </${Button}>
                    ${status.configured &&
                    html`<span class="badge badge-blue">OAuth client configured</span>`}
                  </div>
                </form>
              `}
        </section>

        <section class="card">
          <h2 class="card-title">AI provider</h2>
          <p class="card-sub">Configure an OpenAI-compatible endpoint or Anthropic.</p>
          <p class="muted small">LLM configuration becomes available in a later milestone.</p>
        </section>
      </div>

      ${confirmDisconnect &&
      html`
        <${Modal}
          title="Disconnect Google account"
          onClose=${() => setConfirmDisconnect(false)}
          footer=${html`
            <${Button} variant="secondary" onClick=${() => setConfirmDisconnect(false)}>Cancel</${Button}>
            <${Button} variant="danger" onClick=${disconnect} disabled=${disconnecting}>
              ${disconnecting ? "Disconnecting…" : "Disconnect"}
            </${Button}>
          `}
        >
          <p>
            MailController will delete the stored token and stop accessing
            <strong> ${status && status.email ? status.email : "this mailbox"}</strong>.
          </p>
          <p class="hint">You can reconnect at any time.</p>
        </${Modal}>
      `}
    </div>
  `;
}
