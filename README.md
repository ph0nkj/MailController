# MailController

## What it is

MailController is a small self-hosted web application for managing one Gmail mailbox. It combines bulk mailing, inbox cleanup, a searchable mailbox browser, and an AI assistant with a review-first automation agent. It runs as a single Go binary with an embedded frontend and SQLite database, packaged in a minimal container.

## Features

- Single administrator account with a bcrypt password hash, HttpOnly/SameSite=Lax session cookies, CSRF protection, and login rate limiting.
- Gmail OAuth2 connection. OAuth client credentials and tokens are encrypted at rest with AES-256-GCM.
- Paginated mailbox search with label and unread filters. The UI never renders more than 200 rows.
- Clean All: mark read, archive, or move to Trash, always behind a preview → confirm step.
- Campaigns: paste or upload up to 50,000 recipients with automatic deduplication, text and HTML bodies with an `{{email}}` placeholder, per-recipient status, pause/resume, throttling, and a daily cap.
- Unsubscribe support: `List-Unsubscribe` header plus text and HTML footer, enabled by default.
- AI assistant: summarize selected messages and draft replies. Drafts are never sent automatically.
- Reviewed mailbox agent: turns a natural-language request into a bounded plan, shows it for confirmation, then executes `mark_read`, `mark_unread`, `archive`, or `trash`.
- Audit log for security-relevant actions. Logs never contain passwords, API keys, tokens, or message bodies.
- One static binary, embedded frontend, SQLite storage, and a scratch-based container image.

## Quick start Docker

Requirements: Docker with Compose v2.

```bash
git clone https://github.com/ph0nkj/MailController.git
cd MailController
cp .env.example .env
```

Edit `.env` and set at least:

```bash
MC_SECRET_KEY=...      # generate with: openssl rand -base64 32
MC_ADMIN_PASSWORD=...  # at least 8 characters
```

Build and start the service:

```bash
docker compose up -d --build
```

Open <http://localhost:8080> and sign in with `MC_ADMIN_PASSWORD`.

The container binds to `127.0.0.1:8080` only. For remote access, put it behind an HTTPS reverse proxy, set `MC_BASE_URL` to the public URL, and set `MC_COOKIE_SECURE=true`.

The database lives in the `mailcontroller-data` volume. `docker compose down` preserves it; `docker compose down -v` deletes it.

## Google setup

1. Open the [Google Cloud Console](https://console.cloud.google.com/) and create or select a project.
2. Enable the **Gmail API**.
3. Configure the OAuth consent screen. For personal use, choose External and add your Google account as a test user. Publish the app for long-term use.
4. Create an OAuth client ID of type **Web application**.
5. Add this authorized redirect URI: `MC_BASE_URL` + `/api/v1/google/callback`. For the default local setup that is `http://localhost:8080/api/v1/google/callback`.
6. Copy the client ID and secret into `.env` as `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`, or paste them into **Settings → Google** in the UI. Restart the container after changing `.env`.
7. Open **Settings**, click **Connect Google**, and approve access.

MailController requests only the `gmail.modify` and `gmail.send` scopes. It cannot permanently delete messages.

## LLM setup

The AI assistant and the mailbox agent need an LLM provider. Open **Settings → AI provider**, choose a provider, and enter the model and API key:

- **OpenAI** — default base URL `https://api.openai.com/v1`. Any OpenAI-compatible chat completions endpoint works by changing the base URL.
- **Anthropic** — default base URL `https://api.anthropic.com/v1`, using the Messages API.

The API key is encrypted before it is stored in SQLite. Use **Test connection** to verify the settings.

The assistant can summarize selected messages and draft replies; it never sends mail. The agent turns a request into a plan, but every mailbox-changing action requires preview and explicit confirmation. If an OpenAI-compatible server runs on the Docker host, use an address reachable from the container, for example `http://host.docker.internal:11434/v1`.

## Config table

All runtime settings are environment variables. Docker Compose reads `.env`; the defaults below apply when a variable is unset.

| Variable | Default | Description |
| --- | --- | --- |
| `MC_ADDR` | `:8080` | HTTP listen address inside the container. |
| `MC_BASE_URL` | `http://localhost:8080` | Public URL of the service. Used for OAuth redirects and cookie security. |
| `MC_DB_PATH` | `/data/mc.db` | SQLite database path. Keep it on a persistent volume. |
| `MC_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `MC_SECRET_KEY` | required | Base64-encoded 32-byte AES-256 key. Generate with `openssl rand -base64 32`. |
| `MC_ADMIN_PASSWORD` | required | Initial administrator password, at least 8 characters. |
| `MC_COOKIE_SECURE` | auto | Forces the Secure cookie flag. Defaults to `true` when `MC_BASE_URL` uses HTTPS. |
| `GOOGLE_CLIENT_ID` | empty | Optional OAuth client ID. Can also be saved in Settings. |
| `GOOGLE_CLIENT_SECRET` | empty | Optional OAuth client secret. Can also be saved in Settings. |
| `MC_SEND_RATE_PER_MIN` | `60` | Global cap on campaign messages per minute (1–6000). |
| `MC_DAILY_SEND_LIMIT` | `500` | Global cap on campaign messages per rolling 24 hours (1–100000). |
| `MC_UNSUBSCRIBE_ENABLED` | `true` | Adds a `List-Unsubscribe` header and footer to campaign mail. |
| `MC_UNSUBSCRIBE_EMAIL` | empty | Unsubscribe mailbox. Empty uses the connected Gmail address. |
| `MC_LLM_MAX_MESSAGES` | `200` | Maximum messages one assistant request may inspect (1–1000). |
| `MC_LLM_BODY_CHARS` | `2000` | Maximum body characters per message sent to the LLM (100–100000). |
| `MC_AGENT_MAX_MESSAGES` | `10000` | Maximum messages one agent run may change (1–100000). |
| `MC_AGENT_MAX_STEPS` | `5` | Maximum steps in one agent plan (1–20). |
| `MC_VERSION` | `latest` | Compose image tag and build version label. |

## Limits & safety

- Measured targets: container image about 27 MB (limit 30 MB), idle RSS about 18 MB (limit 50 MB), and frontend about 39 KB gzipped (limit 100 KB).
- Sessions last 12 hours. Five failed logins from one IP within 15 minutes trigger a temporary lockout.
- All state-changing API calls require a CSRF token. Session cookies are HttpOnly and SameSite=Lax.
- Google credentials, tokens, and the LLM API key are encrypted at rest with AES-256-GCM. The admin password is stored as a bcrypt hash.
- Clean All and agent runs use a preview → confirm flow with a one-time token valid for five minutes.
- Agent plans are limited by `MC_AGENT_MAX_MESSAGES` and `MC_AGENT_MAX_STEPS`, and the limits are rechecked before execution. Batches are capped at 1,000 message IDs.
- No operation permanently deletes mail; trashed messages stay in Gmail Trash.
- Campaign delivery is throttled, capped by the environment limits, and retried with backoff on transient errors.
- Email content is treated as untrusted input in LLM prompts. Model output can only produce a restricted set of mailbox actions and is never executed without confirmation.
- Logs and audit entries contain identifiers and counters, not passwords, API keys, tokens, or message bodies.

## Development

Requirements: Go 1.23+, `golangci-lint` 1.62.2, and optionally Docker.

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make lint          # golangci-lint run
make bundle-size   # fail if the gzipped frontend reaches 100 KB
make build         # static binary in bin/mailcontroller
```

Run locally with SQLite in a local directory:

```bash
mkdir -p data
export MC_SECRET_KEY="$(openssl rand -base64 32)"
export MC_ADMIN_PASSWORD=change-me-now
export MC_DB_PATH="$PWD/data/mc.db"
go run ./cmd/mailcontroller
```

Project layout:

- `cmd/mailcontroller` — process entrypoint and service wiring.
- `internal/` — config, auth, crypto, Gmail, store, mailer, LLM, agent, jobs, and HTTP API.
- `web/` — embedded Preact/htm frontend.
- `migrations/` — embedded goose migrations.
- `Dockerfile`, `docker-compose.yml` — container packaging.

## License

Apache License 2.0. See [LICENSE](LICENSE).
