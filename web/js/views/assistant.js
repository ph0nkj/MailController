import { html } from "../html.js";

export function Assistant() {
  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Assistant</h1>
          <p class="page-sub">Describe what you want done; review the plan before anything changes.</p>
        </div>
      </div>
      <div class="card">
        <h2 class="card-title">AI assistant</h2>
        <p class="card-sub">Add an LLM provider key in Settings to use the assistant.</p>
      </div>
    </div>
  `;
}
