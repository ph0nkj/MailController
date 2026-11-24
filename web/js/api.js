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
    if (!headers["Content-Type"]) headers["Content-Type"] = "application/json";
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

  mailList: ({ q = "", label = "", unread = false, pageToken = "", pageSize = 25 }) => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (label) params.set("label", label);
    if (unread) params.set("unread", "true");
    if (pageToken) params.set("page_token", pageToken);
    params.set("page_size", String(pageSize));
    return request(`/mail/messages?${params.toString()}`);
  },
  mailGet: (id) => request(`/mail/messages/${encodeURIComponent(id)}`),
  mailStats: () => request("/mail/stats"),
  mailBulk: (ids, action, confirm = false) =>
    request("/mail/bulk", { method: "POST", body: { ids, action, confirm } }),
  cleanPreview: (mode) =>
    request("/mail/clean/preview", { method: "POST", body: { mode } }),
  cleanExecute: (mode, confirmToken) =>
    request("/mail/clean/execute", {
      method: "POST",
      body: { mode, confirm_token: confirmToken },
    }),
  job: (id) => request(`/jobs/${encodeURIComponent(id)}`),

  campaigns: () => request("/campaigns"),
  campaign: (id) => request(`/campaigns/${encodeURIComponent(id)}`),
  createCampaign: (data) => request("/campaigns", { method: "POST", body: data }),
  updateCampaign: (id, data) =>
    request(`/campaigns/${encodeURIComponent(id)}`, { method: "PUT", body: data }),
  deleteCampaign: (id) =>
    request(`/campaigns/${encodeURIComponent(id)}`, { method: "DELETE" }),
  campaignRecipients: (id, { status = "", page = 1, pageSize = 50 } = {}) => {
    const params = new URLSearchParams();
    if (status) params.set("status", status);
    params.set("page", String(page));
    params.set("page_size", String(pageSize));
    return request(`/campaigns/${encodeURIComponent(id)}/recipients?${params.toString()}`);
  },
  importCampaignRecipients: (id, text) =>
    request(`/campaigns/${encodeURIComponent(id)}/recipients`, {
      method: "POST",
      body: text,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    }),
  startCampaign: (id) =>
    request(`/campaigns/${encodeURIComponent(id)}/start`, {
      method: "POST",
      body: { confirm: true },
    }),
  pauseCampaign: (id) =>
    request(`/campaigns/${encodeURIComponent(id)}/pause`, { method: "POST" }),
};
