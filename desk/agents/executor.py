"""Agent 4: Executor — sizes entries (8% max size) and records paper fills."""

from __future__ import annotations

import json
import os
import time
from typing import Any

import config
from agents import fetch_json


def load_book() -> dict[str, Any]:
    if config.BOOK_FILE.exists():
        try:
            data = json.loads(config.BOOK_FILE.read_text(encoding="utf-8"))
            if isinstance(data, dict) and "cash" in data and "open" in data:
                return data
        except Exception:
            pass
    return {
        "cash": config.EQUITY_START_USD,
        "day_start_equity": config.EQUITY_START_USD,
        "day_date": time.strftime("%Y-%m-%d"),
        "realized_pnl": 0.0,
        "trades_total": 0,
        "wins": 0,
        "open": {},
    }


def save_book(book: dict[str, Any]) -> None:
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    tmp = config.BOOK_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(book, indent=2) + "\n", encoding="utf-8")
    os.replace(tmp, config.BOOK_FILE)


def equity_of(book: dict[str, Any]) -> float:
    open_val = sum(float(pos.get("current_usd", pos.get("usd", 0))) for pos in book.get("open", {}).values())
    return float(book.get("cash", 0)) + open_val


def _jupiter_quote_usd(mint: str, usd_size: float) -> float | None:
    """Best-effort live Jupiter quote for sizing validation (paper mode)."""
    try:
        amount_usdc = max(1_000_000, int(usd_size * 1_000_000))
        url = f"{config.JUPITER_QUOTE_URL}?inputMint={config.USDC_MINT}&outputMint={mint}&amount={amount_usdc}&slippageBps=150"
        q = fetch_json(url, timeout=6.0)
        out_amt = int(q.get("outAmount") or 0)
        return float(out_amt) if out_amt > 0 else None
    except Exception:
        return None


def can_enter(book: dict[str, Any], mint: str) -> tuple[bool, str]:
    if config.STOP_FILE.exists():
        return False, "STOP file active"
    if mint in book.get("open", {}):
        return False, "already holding token"
    if len(book.get("open", {})) >= config.MAX_OPEN_POSITIONS:
        return False, f"max open ({config.MAX_OPEN_POSITIONS}) reached"
    eq = equity_of(book)
    size_usd = round(eq * config.MAX_SIZE_PCT, 2)
    if size_usd < 1.0 or float(book.get("cash", 0)) < size_usd:
        return False, "insufficient paper cash"
    return True, f"size ${size_usd:.2f} (8% eq)"


def execute_entry(
    book: dict[str, Any],
    mint: str,
    facts: dict[str, Any],
    score: int,
    mode: str = "demo",
) -> tuple[bool, str, dict[str, Any] | None]:
    """Open a paper position (or return skip reason)."""
    if not config.PAPER:
        return False, "live wallet execution disabled for safety (set PAPER=True)", None

    ok, reason = can_enter(book, mint)
    if not ok:
        return False, reason, None

    eq = equity_of(book)
    size_usd = round(min(eq * config.MAX_SIZE_PCT, float(book["cash"])), 2)
    entry_price = float(facts.get("price_usd") or 0)
    if entry_price <= 0:
        return False, "invalid entry price", None

    quote_note = "paper spot"
    if mode == "live":
        q = _jupiter_quote_usd(mint, size_usd)
        if q is not None:
            quote_note = "jupiter quote"

    now = time.time()
    pos = {
        "mint": mint,
        "symbol": str(facts.get("symbol") or mint[:6]),
        "entry_price": entry_price,
        "last_price": entry_price,
        "usd": size_usd,
        "current_usd": size_usd,
        "peak_pct": 0.0,
        "stop_pct": config.STOP_PCT,
        "half_taken": False,
        "score": score,
        "opened_at": now,
        "quote": quote_note,
    }
    book["cash"] = round(float(book["cash"]) - size_usd, 4)
    book["open"][mint] = pos
    book["trades_total"] = int(book.get("trades_total", 0)) + 1
    save_book(book)
    return True, f"BUY ${pos['symbol']} ${size_usd:.2f} @ ${entry_price:.6f} ({quote_note})", pos
