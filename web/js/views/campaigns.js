import { html } from "../html.js";

export function Campaigns() {
  return html`
    <div>
      <div class="page-head">
        <div>
          <h1 class="page-title">Campaigns</h1>
          <p class="page-sub">Send to large recipient lists with throttling and progress.</p>
        </div>
      </div>
      <div class="card">
        <h2 class="card-title">Bulk mailing</h2>
        <p class="card-sub">Campaign creation arrives in the bulk mailing milestone.</p>
      </div>
    </div>
  `;
}
