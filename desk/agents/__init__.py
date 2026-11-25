"""6-agent trading desk worker package (stdlib only)."""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any


def fetch_json(url: str, timeout: float = 8.0, headers: dict[str, str] | None = None) -> Any:
    """Fetch JSON over HTTPS using Python stdlib only."""
    req_headers = {
        "User-Agent": "OpenMausBot-Desk/1.0",
        "Accept": "application/json",
    }
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(url, headers=req_headers)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read().decode("utf-8", errors="replace")
        return json.loads(raw)


def post_json(url: str, payload: dict[str, Any], timeout: float = 20.0, headers: dict[str, str] | None = None) -> Any:
    """POST JSON over HTTPS using Python stdlib only."""
    req_headers = {
        "User-Agent": "OpenMausBot-Desk/1.0",
        "Content-Type": "application/json",
        "Accept": "application/json",
    }
    if headers:
        req_headers.update(headers)
    body = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=body, headers=req_headers, method="POST")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read().decode("utf-8", errors="replace")
        return json.loads(raw)
