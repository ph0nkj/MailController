import { useEffect } from "../vendor/hooks.mjs";
import { html } from "../html.js";
import { Link, useLocation } from "../router.js";
import { Button, Icon } from "../components/ui.js";
import { Dashboard } from "./dashboard.js";
import { Mailbox } from "./mailbox.js";
import { Assistant } from "./assistant.js";
import { Campaigns } from "./campaigns.js";
import { Settings } from "./settings.js";

const routes = [
  { path: "/", title: "Dashboard", component: Dashboard, icon: "dashboard", nav: true },
  { path: "/mailbox", title: "Mailbox", component: Mailbox, icon: "mailbox", nav: true },
  { path: "/assistant", title: "Assistant", component: Assistant, icon: "assistant", nav: true },
  { path: "/campaigns", title: "Campaigns", component: Campaigns, icon: "campaigns", nav: true },
  { path: "/settings", title: "Settings", component: Settings, icon: "settings", nav: true },
];

function NotFound() {
  return html`
    <div class="card">
      <h2 class="card-title">Page not found</h2>
      <p class="card-sub">The requested view does not exist.</p>
      <${Link} href="/" className="btn btn-primary">Back to dashboard</${Link}>
    </div>
  `;
}

export function Shell({ onLogout }) {
  const path = useLocation();
  const page = routes.find((route) => route.path === path);
  const Page = page ? page.component : NotFound;

  useEffect(() => {
    document.title = `${page ? page.title : "Not found"} · MailController`;
  }, [page]);

  return html`
    <div class="app-shell">
      <aside class="sidebar">
        <${Link} href="/" className="brand">
          <img class="brand-logo" src="/img/logo.png" alt="" width="36" height="36" />
          <span class="brand-name">MailController</span>
        </${Link}>
        <nav class="nav" aria-label="Main">
          ${routes
            .filter((route) => route.nav)
            .map(
              (route) => html`
                <${Link}
                  href=${route.path}
                  className=${`nav-link ${route.path === path ? "active" : ""}`}
                >
                  <${Icon} name=${route.icon} />
                  <span>${route.title}</span>
                </${Link}>
              `,
            )}
        </nav>
        <div class="sidebar-footer">Self-hosted · Gmail</div>
      </aside>
      <div class="main">
        <header class="topbar">
          <div class="topbar-actions">
            <span class="account-chip">Local admin</span>
            <${Button} variant="ghost" size="sm" onClick=${onLogout} title="Sign out">
              <${Icon} name="logout" size=${16} /> Sign out
            </${Button}>
          </div>
        </header>
        <main class="content">
          <div class="view" key=${path}>
            <${Page} />
          </div>
        </main>
      </div>
    </div>
  `;
}
