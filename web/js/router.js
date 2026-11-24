import { useState, useEffect } from "./vendor/hooks.mjs";
import { html } from "./html.js";

export function navigate(path) {
  if (window.location.pathname === path) return;
  window.history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

export function useLocation() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onChange = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onChange);
    return () => window.removeEventListener("popstate", onChange);
  }, []);
  return path;
}

export function Link({ href, className, children, onClick }) {
  return html`
    <a
      href=${href}
      className=${className}
      onClick=${(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) {
          return;
        }
        event.preventDefault();
        if (onClick) onClick(event);
        navigate(href);
      }}
    >
      ${children}
    </a>
  `;
}
