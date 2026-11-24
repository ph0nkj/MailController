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
          <${AIProviderCard} />
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

const AI_PROVIDERS = {
  openai: {
    label: "OpenAI compatible",
    base_url: "https://api.openai.com/v1",
    model_placeholder: "gpt-4o-mini",
  },
  anthropic: {
    label: "Anthropic",
    base_url: "https://api.anthropic.com/v1",
    model_placeholder: "claude-3-5-sonnet-latest",
  },
};

function AIProviderCard() {
  const [status, setStatus] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [provider, setProvider] = useState("openai");
  const [baseURL, setBaseURL] = useState(AI_PROVIDERS.openai.base_url);
  const [model, setModel] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [formError, setFormError] = useState("");
  const [testResult, setTestResult] = useState(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);

  useEffect(() => {
    let active = true;
    api
      .llmStatus()
      .then((next) => {
        if (!active) return;
        const nextProvider = AI_PROVIDERS[next.provider] ? next.provider : "openai";
        setStatus(next);
        setProvider(nextProvider);
        setBaseURL(next.base_url || AI_PROVIDERS[nextProvider].base_url);
        setModel(next.model || "");
        setAPIKey("");
      })
      .catch((err) => {
        if (active) setLoadError(err.message || "Could not load AI provider status");
      });
    return () => {
      active = false;
    };
  }, []);

  const meta = AI_PROVIDERS[provider] || AI_PROVIDERS.openai;

  function changeProvider(event) {
    const next = event.target.value;
    const previousDefault = AI_PROVIDERS[provider]?.base_url;
    setProvider(next);
    if (!baseURL.trim() || baseURL.trim() === previousDefault) {
      setBaseURL(AI_PROVIDERS[next].base_url);
    }
    setFormError("");
    setTestResult(null);
  }

  async function saveProvider(event) {
    event.preventDefault();
    if (saving) return;
    setFormError("");
    setTestResult(null);
    if (!model.trim()) {
      setFormError("A model name is required.");
      return;
    }
    if (!apiKey.trim() && !(status && status.has_api_key)) {
      setFormError("An API key is required the first time you configure a provider.");
      return;
    }
    setSaving(true);
    try {
      const next = await api.saveLLMSettings({
        provider,
        base_url: baseURL.trim(),
        model: model.trim(),
        api_key: apiKey.trim(),
      });
      setStatus(next);
      setAPIKey("");
      toast("AI provider saved", "success");
    } catch (err) {
      setFormError(err.message || "Could not save AI provider settings");
    } finally {
      setSaving(false);
    }
  }

  async function testProvider() {
    if (testing) return;
    setTesting(true);
    setFormError("");
    setTestResult(null);
    try {
      const next = await api.testLLM();
      setStatus(next);
      setTestResult({ ok: true, message: "The provider responded successfully." });
      toast("AI provider responded", "success");
    } catch (err) {
      setTestResult({ ok: false, message: err.message || "The provider did not respond" });
    } finally {
      setTesting(false);
    }
  }

  async function removeProvider() {
    if (removing) return;
    setRemoving(true);
    setFormError("");
    try {
      await api.clearLLMSettings();
      setStatus({
        configured: false,
        provider: "openai",
        base_url: AI_PROVIDERS.openai.base_url,
        model: "",
        has_api_key: false,
      });
      setProvider("openai");
      setBaseURL(AI_PROVIDERS.openai.base_url);
      setModel("");
      setAPIKey("");
      setTestResult(null);
      setConfirmRemove(false);
      toast("AI provider removed", "success");
    } catch (err) {
      setFormError(err.message || "Could not remove AI provider settings");
    } finally {
      setRemoving(false);
    }
  }

  return html`
    <div>
      <div class="row-between">
        <div>
          <h2 class="card-title">AI provider</h2>
          <p class="card-sub">Summarize mail and draft replies with an OpenAI-compatible endpoint or Anthropic.</p>
        </div>
        ${status &&
        html`
          <span class=${`badge ${status.configured ? "badge-green" : "badge-gray"}`}>
            ${status.configured ? "Configured" : "Not configured"}
          </span>
        `}
      </div>

      <${ErrorBox} message=${loadError} />
      ${!status
        ? html`<${LoadingRow} label="Loading AI provider status…" />`
        : html`
            <form onSubmit=${saveProvider} class="mt-4">
              <div class="form-grid">
                <div class="field">
                  <label class="label" for="ai-provider">Provider</label>
                  <select
                    id="ai-provider"
                    class="select"
                    value=${provider}
                    onChange=${changeProvider}
                    disabled=${saving}
                  >
                    ${Object.entries(AI_PROVIDERS).map(
                      ([value, info]) => html`<option value=${value}>${info.label}</option>`,
                    )}
                  </select>
                </div>
                <div class="field">
                  <label class="label" for="ai-model">Model</label>
                  <input
                    id="ai-model"
                    class="input"
                    value=${model}
                    onInput=${(event) => setModel(event.target.value)}
                    placeholder=${meta.model_placeholder}
                    autocomplete="off"
                    disabled=${saving}
                  />
                </div>
                <div class="field">
                  <label class="label" for="ai-base-url">Base URL</label>
                  <input
                    id="ai-base-url"
                    class="input mono"
                    value=${baseURL}
                    onInput=${(event) => setBaseURL(event.target.value)}
                    placeholder=${meta.base_url}
                    autocomplete="off"
                    spellcheck="false"
                    disabled=${saving}
                  />
                  <p class="hint">Use the API root, for example ${meta.base_url}.</p>
                </div>
                <div class="field">
                  <label class="label" for="ai-api-key">API key</label>
                  <input
                    id="ai-api-key"
                    class="input"
                    type="password"
                    value=${apiKey}
                    onInput=${(event) => setAPIKey(event.target.value)}
                    placeholder=${status.has_api_key ? "Saved — enter a new key to replace" : "sk-…"}
                    autocomplete="new-password"
                    disabled=${saving}
                  />
                  <p class="hint">Encrypted at rest with AES-256-GCM and never displayed again.</p>
                </div>
              </div>

              <${ErrorBox} message=${formError} />
              ${testResult &&
              html`
                <p class=${`ai-test-result ${testResult.ok ? "ok" : "error"}`} role="status">
                  ${testResult.message}
                </p>
              `}

              <div class="row ai-settings-actions">
                <${Button} type="submit" variant="primary" disabled=${saving}>
                  ${saving ? html`<${Spinner} /> Saving…` : "Save provider"}
                </${Button}>
                <${Button}
                  variant="secondary"
                  onClick=${testProvider}
                  disabled=${testing || saving || !status.configured}
                  title=${status.configured ? "Send a minimal request" : "Save the provider first"}
                >
                  ${testing ? html`<${Spinner} /> Testing…` : "Test connection"}
                </${Button}>
                ${(status.configured || status.has_api_key) &&
                html`
                  <${Button}
                    variant="danger-outline"
                    onClick=${() => setConfirmRemove(true)}
                    disabled=${removing || saving}
                  >
                    Remove provider
                  </${Button}>
                `}
              </div>
            </form>
          `}

      <p class="hint ai-privacy-note">
        Email content is sent to the provider only when you run Summarize or Draft reply.
        Messages are treated as untrusted data and the assistant cannot change or send mail.
      </p>

      ${confirmRemove &&
      html`
        <${Modal}
          title="Remove AI provider"
          onClose=${() => setConfirmRemove(false)}
          footer=${html`
            <${Button} variant="secondary" onClick=${() => setConfirmRemove(false)}>Cancel</${Button}>
            <${Button} variant="danger" onClick=${removeProvider} disabled=${removing}>
              ${removing ? "Removing…" : "Remove provider"}
            </${Button}>
          `}
        >
          <p>Delete the stored AI provider settings and API key?</p>
          <p class="hint">Summaries and reply drafts will be unavailable until you configure a provider again.</p>
        </${Modal}>
      `}
    </div>
  `;
}
