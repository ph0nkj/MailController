#!/usr/bin/env python3
"""Minimal OpenAI/Anthropic-compatible mock used by the local UI tests.

It never logs request headers or bodies. A valid test key is required so the
UI's "Connected" and "Not connected" states can both be exercised.
"""

from __future__ import annotations

import argparse
import json
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

VALID_OPENAI_KEY = "sk-test-valid-key"
VALID_ANTHROPIC_KEY = "sk-ant-test-valid-key"


class Handler(BaseHTTPRequestHandler):
    server_version = "MailControllerMockLLM/1.0"

    def log_message(self, fmt: str, *args: object) -> None:  # noqa: A003
        # Keep test output quiet; never print headers or request bodies.
        return

    def _send(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/health":
            self._send(HTTPStatus.OK, {"status": "ok"})
            return
        self._send(HTTPStatus.NOT_FOUND, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length > 1 << 20:
            self._send(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, {"error": "body too large"})
            return
        try:
            json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError:
            self._send(HTTPStatus.BAD_REQUEST, {"error": "invalid json"})
            return

        if self.path.endswith("/chat/completions"):
            if self.headers.get("Authorization") != f"Bearer {VALID_OPENAI_KEY}":
                self._send(HTTPStatus.UNAUTHORIZED, {"error": "invalid api key"})
                return
            self._send(
                HTTPStatus.OK,
                {"choices": [{"message": {"role": "assistant", "content": "OK"}}]},
            )
            return

        if self.path.endswith("/messages"):
            if self.headers.get("x-api-key") != VALID_ANTHROPIC_KEY:
                self._send(HTTPStatus.UNAUTHORIZED, {"error": "invalid api key"})
                return
            self._send(
                HTTPStatus.OK,
                {"content": [{"type": "text", "text": "OK"}]},
            )
            return

        self._send(HTTPStatus.NOT_FOUND, {"error": "not found"})


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=18090)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
