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
    default_model: "gpt-4o-mini",
  },
  anthropic: {
    label: "Anthropic",
    base_url: "https://api.anthropic.com/v1",
    default_model: "claude-sonnet-4-5",
  },
};

function detectProvider(apiKey, fallback) {
  const value = apiKey.trim();
  if (value.startsWith("sk-ant-")) return "anthropic";
  if (value.startsWith("sk-")) return "openai";
  return fallback || "openai";
}

function AIProviderCard() {
  const [status, setStatus] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [provider, setProvider] = useState("openai");
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [formError, setFormError] = useState("");
  const [connection, setConnection] = useState({ state: "idle", message: "" });
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);

  useEffect(() => {
    let active = true;
    api
      .llmStatus()
      .then(async (next) => {
        if (!active) return;
        const nextProvider = AI_PROVIDERS[next.provider] ? next.provider : "openai";
        setStatus(next);
        setProvider(nextProvider);
        setBaseURL(next.base_url || AI_PROVIDERS[nextProvider].base_url);
        setModel(next.model || "");
        setAPIKey("");
        if (!next.configured) return;
        setConnection({ state: "checking", message: "" });
        try {
          const tested = await api.testLLM();
          if (!active) return;
          setStatus(tested);
          setConnection({ state: "ok", message: "Provider responded successfully." });
        } catch (err) {
          if (!active) return;
          setConnection({
            state: "error",
            message: err.message || "The provider did not respond.",
          });
        }
      })
      .catch((err) => {
        if (active) setLoadError(err.message || "Could not load AI provider status");
      });
    return () => {
      active = false;
    };
  }, []);

  const meta = AI_PROVIDERS[provider] || AI_PROVIDERS.openai;
  const connectionState = connection.state;
  const connectionLabel =
    connectionState === "ok"
      ? "Connected"
      : connectionState === "error"
        ? "Not connected"
        : connectionState === "checking"
          ? "Checking…"
          : status && status.configured
            ? "Not checked"
            : "Not configured";
  const connectionBadge =
    connectionState === "ok"
      ? "badge-green"
      : connectionState === "error"
        ? "badge-red"
        : connectionState === "checking"
          ? "badge-blue"
          : status && status.configured
            ? "badge-amber"
            : "badge-gray";

  function changeProvider(event) {
    const next = event.target.value;
    const nextMeta = AI_PROVIDERS[next];
    const previousMeta = AI_PROVIDERS[provider];
    setProvider(next);
    setBaseURL((current) =>
      !current.trim() || current.trim() === previousMeta.base_url ? nextMeta.base_url : current,
    );
    setModel((current) =>
      !current.trim() || current.trim() === previousMeta.default_model
        ? nextMeta.default_model
        : current,
    );
    setFormError("");
    setConnection({ state: "idle", message: "" });
  }

  async function saveProvider(event) {
    event.preventDefault();
    if (saving) return;
    setFormError("");
    const key = apiKey.trim();
    if (!key && !(status && status.has_api_key)) {
      setFormError("Paste an API key to connect a provider.");
      return;
    }
    const nextProvider = key ? detectProvider(key, provider) : provider;
    const previousMeta = AI_PROVIDERS[provider] || AI_PROVIDERS.openai;
    const nextMeta = AI_PROVIDERS[nextProvider];
    let nextBaseURL = baseURL.trim();
    let nextModel = model.trim();
    if (!nextBaseURL || (nextProvider !== provider && nextBaseURL === previousMeta.base_url)) {
      nextBaseURL = nextMeta.base_url;
    }
    if (!nextModel || (nextProvider !== provider && nextModel === previousMeta.default_model)) {
      nextModel = nextMeta.default_model;
    }
    setSaving(true);
    try {
      const next = await api.saveLLMSettings({
        provider: nextProvider,
        base_url: nextBaseURL,
        model: nextModel,
        api_key: key,
      });
      setStatus(next);
      setProvider(nextProvider);
      setBaseURL(nextBaseURL);
      setModel(nextModel);
      setAPIKey("");
      setConnection({ state: "checking", message: "" });
      try {
        const tested = await api.testLLM();
        setStatus(tested);
        setConnection({ state: "ok", message: "Provider responded successfully." });
        toast("AI provider connected", "success");
      } catch (err) {
        setConnection({
          state: "error",
          message: err.message || "The provider did not respond.",
        });
        toast("Saved, but the connection test failed", "error");
      }
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
    setConnection({ state: "checking", message: "" });
    try {
      const next = await api.testLLM();
      setStatus(next);
      setConnection({ state: "ok", message: "Provider responded successfully." });
      toast("AI provider connected", "success");
    } catch (err) {
      setConnection({
        state: "error",
        message: err.message || "The provider did not respond.",
      });
      toast("AI provider connection failed", "error");
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
      setConnection({ state: "idle", message: "" });
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
          <p class="card-sub">
            Paste one API key. MailController detects the provider and checks the connection.
          </p>
        </div>
        ${status &&
        html`
          <span class=${`badge ${connectionBadge}`}>${connectionLabel}</span>
        `}
      </div>

      <${ErrorBox} message=${loadError} />
      ${!status
        ? html`<${LoadingRow} label="Loading AI provider status…" />`
        : html`
            <form onSubmit=${saveProvider} class="mt-4">
              <div class="field">
                <label class="label" for="ai-api-key">API key</label>
                <input
                  id="ai-api-key"
                  class="input"
                  type="password"
                  value=${apiKey}
                  onInput=${(event) => {
                    setAPIKey(event.target.value);
                    setFormError("");
                    if (connection.state !== "idle") {
                      setConnection({ state: "idle", message: "" });
                    }
                  }}
                  placeholder=${status.has_api_key ? "Saved — paste a new key to replace" : "sk-… or sk-ant-…"}
                  autocomplete="new-password"
                  spellcheck="false"
                  disabled=${saving}
                />
                <p class="hint">
                  Paste an OpenAI or Anthropic key. The provider and model are chosen automatically.
                  The key is encrypted at rest and never displayed again.
                </p>
              </div>

              ${connection.state === "error" &&
              html`
                <p class="ai-test-result error" role="status">
                  ${connection.message || "The provider did not respond."}
                </p>
              `}
              ${connection.state === "ok" &&
              html`
                <p class="ai-test-result ok" role="status">
                  Connected${status && status.model ? ` · ${status.model}` : ""}.
                </p>
              `}

              <${ErrorBox} message=${formError} />

              <div class="row ai-settings-actions">
                <${Button} type="submit" variant="primary" disabled=${saving}>
                  ${saving
                    ? html`<${Spinner} /> Saving…`
                    : status && status.configured
                      ? "Save & test"
                      : "Connect"}
                </${Button}>
                <${Button}
                  variant="secondary"
                  onClick=${testProvider}
                  disabled=${testing || saving || !(status && status.configured)}
                  title=${status && status.configured ? "Send a minimal request" : "Connect an API key first"}
                >
                  ${testing ? html`<${Spinner} /> Checking…` : "Check connection"}
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

              <details class="ai-advanced">
                <summary>Advanced settings</summary>
                <p class="hint">
                  Optional. Set a provider, model or custom endpoint only when the defaults do not fit.
                </p>
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
                      placeholder=${meta.default_model}
                      autocomplete="off"
                      spellcheck="false"
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
                </div>
              </details>
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
