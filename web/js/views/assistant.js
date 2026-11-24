import { html } from "../html.js";
import { Link } from "../router.js";
import { Icon } from "../components/ui.js";

function Capability({ icon, title, children, action }) {
  return html`
    <section class="card ai-capability">
      <div class="ai-capability-icon"><${Icon} name=${icon} size=${20} /></div>
      <div>
        <h2 class="card-title">${title}</h2>
        <p class="card-sub">${children}</p>
        ${action}
      </div>
    </section>
  `;
}

export function Assistant() {
  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Assistant</h1>
          <p class="page-sub">Read-only AI help for your mailbox. Nothing is changed or sent.</p>
        </div>
      </div>

      <div class="stack">
        <section class="card ai-hero">
          <h2 class="card-title">Understand mail faster</h2>
          <p class="card-sub">
            Open any message in the mailbox and ask for a summary or a reply draft. Email content is
            treated as untrusted data, and the assistant cannot execute actions.
          </p>
          <div class="row">
            <${Link} href="/mailbox" className="btn btn-primary">Open mailbox</${Link}>
            <${Link} href="/settings" className="btn btn-secondary">AI settings</${Link}>
          </div>
        </section>

        <div class="ai-capability-grid">
          <${Capability} icon="mailbox" title="Summarize a message">
            Get the key points, deadlines and requests from a single email without reading the full
            thread.
          </${Capability}>
          <${Capability} icon="assistant" title="Draft a reply">
            Generate a plain-text response you can review and copy into Gmail. Drafts are never sent
            automatically.
          </${Capability}>
          <${Capability} icon="settings" title="Bring your own provider">
            Connect an OpenAI-compatible endpoint or Anthropic with an encrypted API key.
          </${Capability}>
        </div>

        <section class="card">
          <h2 class="card-title">Guardrails</h2>
          <ul class="ai-guardrails">
            <li>Messages are sent to the provider only when you explicitly run an AI action.</li>
            <li>Email bodies are truncated and labelled as untrusted data in every prompt.</li>
            <li>Secrets are encrypted at rest and never appear in logs or API responses.</li>
            <li>Bulk agent actions are not enabled yet; the assistant stays read-only.</li>
          </ul>
        </section>
      </div>
    </div>
  `;
}
