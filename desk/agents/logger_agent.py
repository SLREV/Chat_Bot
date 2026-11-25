"""Agent 6: Logger — persists structured desk_log.jsonl and atomic state.json."""

from __future__ import annotations

import json
import os
import time
from typing import Any

import config
from agents.executor import equity_of
from agents.filter_agent import RULE_LABELS


def default_state() -> dict[str, Any]:
    return {
        "updated_at": time.time(),
        "mode": "demo",
        "paper": config.PAPER,
        "stopped": config.STOP_FILE.exists(),
        "stop_reason": "",
        "equity_usd": config.EQUITY_START_USD,
        "start_equity_usd": config.EQUITY_START_USD,
        "pnl_usd": 0.0,
        "pnl_pct": 0.0,
        "multiplier": 1.0,
        "scanned": 0,
        "traded": 0,
        "scored_pass": 0,
        "killed_total": 0,
        "filter_error_streak": 0,
        "killed_by_rule": {k: 0 for k in RULE_LABELS},
        "agents": {
            "scout": {"status": "idle", "last": "Waiting for pass", "badge": "0 pairs"},
            "filter": {"status": "idle", "last": "8 kill rules armed", "badge": "0 killed"},
            "analyst": {"status": "idle", "last": f"score {config.MIN_ANALYST_SCORE}+ only", "badge": "0 scored"},
            "executor": {"status": "idle", "last": "8% max size (paper)", "badge": "size 8%"},
            "risk": {"status": "idle", "last": f"{config.STOP_PCT:.0f}% stop · trail +12%", "badge": "-6% · trail +12%"},
            "logger": {"status": "idle", "last": "reason for all", "badge": "0 events"},
        },
        "ticker": [],
        "open_positions": [],
        "logs": [],
    }


def load_state() -> dict[str, Any]:
    if config.STATE_FILE.exists():
        try:
            data = json.loads(config.STATE_FILE.read_text(encoding="utf-8"))
            if isinstance(data, dict) and "agents" in data:
                # Ensure all rule keys exist
                kr = data.setdefault("killed_by_rule", {})
                for k in RULE_LABELS:
                    kr.setdefault(k, 0)
                return data
        except Exception:
            pass
    return default_state()


def save_state(state: dict[str, Any]) -> None:
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    state["updated_at"] = time.time()
    tmp = config.STATE_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(state, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, config.STATE_FILE)


def append_log(entry: dict[str, Any]) -> dict[str, Any]:
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    now = time.time()
    rec = {
        "ts": now,
        "hhmm": time.strftime("%H:%M", time.localtime(now)),
        "agent": entry.get("agent", "LOGGER"),
        "level": entry.get("level", "info"),
        "symbol": entry.get("symbol", ""),
        "msg": entry.get("msg", ""),
    }
    with config.LOG_FILE.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(rec) + "\n")
    return rec


def sync_book_metrics(state: dict[str, Any], book: dict[str, Any]) -> None:
    eq = round(equity_of(book), 2)
    start = float(book.get("day_start_equity") or config.EQUITY_START_USD)
    pnl = round(eq - config.EQUITY_START_USD, 2)
    pnl_pct = round((pnl / config.EQUITY_START_USD) * 100.0, 2) if config.EQUITY_START_USD > 0 else 0.0
    mult = round(eq / config.EQUITY_START_USD, 2) if config.EQUITY_START_USD > 0 else 1.0

    state["equity_usd"] = eq
    state["start_equity_usd"] = round(start, 2)
    state["pnl_usd"] = pnl
    state["pnl_pct"] = pnl_pct
    state["multiplier"] = mult
    state["traded"] = int(book.get("trades_total", 0))
    state["stopped"] = config.STOP_FILE.exists()
    state["stop_reason"] = config.STOP_FILE.read_text(encoding="utf-8").strip() if state["stopped"] else ""

    open_list = []
    for mint, pos in book.get("open", {}).items():
        entry = float(pos["entry_price"])
        last = float(pos["last_price"])
        gain_pct = round(((last - entry) / entry) * 100.0, 2) if entry > 0 else 0.0
        open_list.append({
            "mint": mint,
            "symbol": pos["symbol"],
            "entry_price": entry,
            "last_price": last,
            "usd": round(float(pos["usd"]), 2),
            "current_usd": round(float(pos["current_usd"]), 2),
            "gain_pct": gain_pct,
            "stop_pct": float(pos["stop_pct"]),
            "score": int(pos.get("score", 0)),
        })
    state["open_positions"] = open_list
