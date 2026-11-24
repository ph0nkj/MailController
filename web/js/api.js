let csrfToken = "";

export function setCSRFToken(token) {
  csrfToken = token || "";
}

export function getCSRFToken() {
  return csrfToken;
}

export class ApiError extends Error {
  constructor(code, message, status) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

export async function request(path, options = {}) {
  const method = (options.method || "GET").toUpperCase();
  const headers = {
    Accept: "application/json",
    ...(options.headers || {}),
  };
  let body = options.body;
  if (body !== undefined && body !== null && !(body instanceof FormData)) {
    if (typeof body !== "string") body = JSON.stringify(body);
    headers["Content-Type"] = "application/json";
  }
  if (csrfToken && !["GET", "HEAD", "OPTIONS"].includes(method)) {
    headers["X-CSRF-Token"] = csrfToken;
  }

  const response = await fetch(`/api/v1${path}`, {
    ...options,
    method,
    headers,
    body,
    credentials: "same-origin",
  });

  let data = null;
  const text = await response.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { error: { code: "invalid_response", message: text } };
    }
  }

  if (!response.ok) {
    const err = data && data.error ? data.error : {};
    if (response.status === 401 && !path.startsWith("/auth/")) {
      window.dispatchEvent(new CustomEvent("mc:unauthorized"));
    }
    throw new ApiError(
      err.code || "request_failed",
      err.message || `Request failed (${response.status})`,
      response.status,
    );
  }
  return data;
}

export const api = {
  login: (password) =>
    request("/auth/login", { method: "POST", body: { password } }),
  logout: () => request("/auth/logout", { method: "POST" }),
  me: () => request("/auth/me"),

  googleStatus: () => request("/google/status"),
  saveGoogleClient: (clientId, clientSecret) =>
    request("/google/client", {
      method: "PUT",
      body: { client_id: clientId, client_secret: clientSecret },
    }),
  disconnectGoogle: () => request("/google/disconnect", { method: "DELETE" }),
};
