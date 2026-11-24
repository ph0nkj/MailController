import { render } from "./vendor/preact.mjs";
import { useState, useEffect } from "./vendor/hooks.mjs";
import { html } from "./html.js";
import { api, setCSRFToken } from "./api.js";
import { Spinner, Toaster } from "./components/ui.js";
import { Login } from "./views/login.js";
import { Shell } from "./views/shell.js";

function App() {
  const [loading, setLoading] = useState(true);
  const [authenticated, setAuthenticated] = useState(false);

  useEffect(() => {
    let active = true;
    api
      .me()
      .then((me) => {
        if (!active) return;
        setCSRFToken(me.csrf_token);
        setAuthenticated(true);
      })
      .catch(() => {
        if (!active) return;
        setAuthenticated(false);
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    const onUnauthorized = () => {
      setCSRFToken("");
      setAuthenticated(false);
    };
    window.addEventListener("mc:unauthorized", onUnauthorized);
    return () => {
      active = false;
      window.removeEventListener("mc:unauthorized", onUnauthorized);
    };
  }, []);

  async function logout() {
    try {
      await api.logout();
    } catch {
      // Session may already be invalid; the UI must still return to login.
    }
    setCSRFToken("");
    setAuthenticated(false);
    window.history.pushState({}, "", "/");
  }

  if (loading) {
    return html`<div class="boot"><${Spinner} /></div>`;
  }

  return html`
    <${authenticated ? Shell : Login}
      onLogin=${() => setAuthenticated(true)}
      onLogout=${logout}
    />
    <${Toaster} />
  `;
}

render(html`<${App} />`, document.getElementById("app"));
