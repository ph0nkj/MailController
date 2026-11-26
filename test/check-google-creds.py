#!/usr/bin/env python3
"""Validate the Google OAuth client credentials from .env.

A deliberately invalid authorization code is sent to Google's token endpoint.
`invalid_grant` means the client id/secret were accepted; `invalid_client` means
they were rejected. The script prints only the OAuth error, never credentials.
"""

from __future__ import annotations

import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


def read_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip('"').strip("'")
    return values


def main() -> int:
    env = read_env(Path(__file__).resolve().parent.parent / ".env")
    client_id = env.get("GOOGLE_CLIENT_ID", "")
    client_secret = env.get("GOOGLE_CLIENT_SECRET", "")
    base_url = env.get("MC_BASE_URL", "http://localhost:8080").rstrip("/")
    if not client_id or not client_secret:
        print("google_credentials=missing")
        return 2

    data = urllib.parse.urlencode(
        {
            "client_id": client_id,
            "client_secret": client_secret,
            "code": "mc-test-invalid-code",
            "grant_type": "authorization_code",
            "redirect_uri": base_url + "/api/v1/google/callback",
        }
    ).encode()
    request = urllib.request.Request(
        "https://oauth2.googleapis.com/token",
        data=data,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            print(f"google_token_http={response.status}")
            return 1
    except urllib.error.HTTPError as err:
        try:
            payload = json.loads(err.read().decode("utf-8", "replace"))
        except json.JSONDecodeError:
            payload = {}
        error = payload.get("error", "unknown")
        description = payload.get("error_description", "")
        print(f"google_token_http={err.code}")
        print(f"google_oauth_error={error}")
        if description:
            print(f"google_oauth_description={description}")
        if error == "invalid_grant":
            print("google_client_credentials=accepted")
            return 0
        print("google_client_credentials=rejected")
        return 1
    except Exception as exc:  # noqa: BLE001 - report connectivity only
        print(f"google_token_error={type(exc).__name__}")
        return 2


if __name__ == "__main__":
    sys.exit(main())
