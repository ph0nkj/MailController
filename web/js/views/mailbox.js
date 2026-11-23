import { html } from "../html.js";

export function Mailbox() {
  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Mailbox</h1>
          <p class="page-sub">Search and manage messages from your Gmail account.</p>
        </div>
      </div>
      <div class="card">
        <h2 class="card-title">Mailbox view</h2>
        <p class="card-sub">Connect a Google account to load messages.</p>
      </div>
    </div>
  `;
}
