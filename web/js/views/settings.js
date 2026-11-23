import { html } from "../html.js";

export function Settings() {
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
          <h2 class="card-title">Google account</h2>
          <p class="card-sub">OAuth client credentials and account connection.</p>
          <p class="muted small">Google integration becomes available in the next milestone.</p>
        </section>
        <section class="card">
          <h2 class="card-title">AI provider</h2>
          <p class="card-sub">Configure an OpenAI-compatible endpoint or Anthropic.</p>
          <p class="muted small">LLM configuration becomes available in a later milestone.</p>
        </section>
      </div>
    </div>
  `;
}
