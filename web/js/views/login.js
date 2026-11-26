import { useState } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { api, setCSRFToken } from "../api.js";
import { Button, ErrorBox } from "../components/ui.js";

export function Login({ onLogin }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event) {
    event.preventDefault();
    if (busy) return;
    setError("");
    setBusy(true);
    try {
      const result = await api.login(password);
      setCSRFToken(result.csrf_token);
      onLogin();
    } catch (err) {
      setError(err.message || "Could not sign in");
    } finally {
      setBusy(false);
    }
  }

  return html`
    <main class="auth-page">
      <div class="auth-card">
        <div class="auth-brand">
          <img class="auth-logo" src="/img/logo.png" alt="" width="64" height="64" />
          <span>MailController</span>
        </div>
        <h1 class="auth-title">Sign in</h1>
        <p class="auth-sub">Use the administrator password from your environment.</p>
        <form onSubmit=${submit}>
          <${ErrorBox} message=${error} />
          <input
            class="visually-hidden"
            type="text"
            name="username"
            value="admin"
            autocomplete="username"
            readonly
            tabindex="-1"
            aria-hidden="true"
          />
          <div class="field">
            <label class="label" for="password">Admin password</label>
            <input
              id="password"
              class="input"
              type="password"
              name="password"
              autocomplete="current-password"
              required
              autofocus
              value=${password}
              disabled=${busy}
              onInput=${(event) => setPassword(event.target.value)}
            />
          </div>
          <${Button} type="submit" variant="primary" className="btn-block btn-lg" disabled=${busy}>
            ${busy ? "Signing in…" : "Sign in"}
          </${Button}>
        </form>
        <p class="auth-footer">Self-hosted · Your credentials never leave this server.</p>
      </div>
    </main>
  `;
}
