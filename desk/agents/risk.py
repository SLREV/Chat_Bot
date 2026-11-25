"""Agent 5: Risk — enforces -6% stop, trailing stop, take-half, and -15% day stop."""

from __future__ import annotations

import random
import time
from typing import Any

import config
from agents import fetch_json
from agents.executor import equity_of, save_book


def _live_price(mint: str) -> float | None:
    try:
        dex = fetch_json(config.DEXSCREENER_PAIRS_URL.format(mint=mint), timeout=6.0)
        pairs = dex.get("pairs") if isinstance(dex, dict) else None
        if not pairs:
            return None
        best = max(pairs, key=lambda p: float((p.get("liquidity") or {}).get("usd") or 0))
        val = float(best.get("priceUsd") or 0)
        return val if val > 0 else None
    except Exception:
        return None


def _trail_stop_for(peak_pct: float, current_stop: float) -> float:
    floor = current_stop
    for peak_gate, new_stop in config.TRAIL_LEVELS:
        if peak_pct >= peak_gate and new_stop > floor:
            floor = new_stop
    return floor


def monitor_positions(
    book: dict[str, Any],
    mode: str = "demo",
    rng: random.Random | None = None,
) -> list[dict[str, Any]]:
    """Evaluate all open positions and apply stops / take-profit / day breaker."""
    events: list[dict[str, Any]] = []
    now = time.time()
    r = rng or random

    today = time.strftime("%Y-%m-%d")
    if book.get("day_date") != today:
        book["day_date"] = today
        book["day_start_equity"] = round(equity_of(book), 4)

    to_close: list[tuple[str, str, float]] = []
    for mint, pos in list(book.get("open", {}).items()):
        entry = float(pos["entry_price"])
        last = float(pos.get("last_price", entry))

        if mode == "live":
            px = _live_price(mint)
            if px is not None:
                last = px
        else:
            # Gentle random walk biased slightly positive for high-score entries
            step = r.uniform(-0.075, 0.095)
            last = max(0.000001, round(last * (1.0 + step), 6))

        pos["last_price"] = last
        gain_pct = ((last - entry) / entry) * 100.0 if entry > 0 else 0.0
        pos["peak_pct"] = max(float(pos.get("peak_pct", 0.0)), gain_pct)
        pos["stop_pct"] = _trail_stop_for(pos["peak_pct"], float(pos.get("stop_pct", config.STOP_PCT)))
        pos["current_usd"] = round(float(pos["usd"]) * (1.0 + gain_pct / 100.0), 4)

        # Take half at +100%
        if not pos.get("half_taken") and gain_pct >= config.TAKE_HALF_AT_PCT:
            half_proceeds = round(pos["current_usd"] * 0.5, 4)
            half_cost = round(float(pos["usd"]) * 0.5, 4)
            book["cash"] = round(float(book["cash"]) + half_proceeds, 4)
            book["realized_pnl"] = round(float(book.get("realized_pnl", 0.0)) + (half_proceeds - half_cost), 4)
            pos["usd"] = round(float(pos["usd"]) - half_cost, 4)
            pos["current_usd"] = round(pos["current_usd"] - half_proceeds, 4)
            pos["half_taken"] = True
            events.append({
                "agent": "RISK",
                "level": "ok",
                "symbol": pos["symbol"],
                "msg": f"TAKE-HALF ${pos['symbol']} +{gain_pct:.1f}% (+${half_proceeds - half_cost:.2f})",
            })

        age_hours = (now - float(pos.get("opened_at", now))) / 3600.0
        if gain_pct <= float(pos["stop_pct"]):
            why = "hard stop -6%" if float(pos["stop_pct"]) < 0 else f"trail stop +{float(pos['stop_pct']):.0f}%"
            to_close.append((mint, why, gain_pct))
        elif age_hours >= config.MAX_HOLD_HOURS and float(pos["stop_pct"]) < 0:
            to_close.append((mint, f"max hold {config.MAX_HOLD_HOURS:.0f}h", gain_pct))

    for mint, why, gain_pct in to_close:
        pos = book["open"].pop(mint)
        proceeds = float(pos["current_usd"])
        pnl = round(proceeds - float(pos["usd"]), 4)
        book["cash"] = round(float(book["cash"]) + proceeds, 4)
        book["realized_pnl"] = round(float(book.get("realized_pnl", 0.0)) + pnl, 4)
        if pnl > 0:
            book["wins"] = int(book.get("wins", 0)) + 1
        events.append({
            "agent": "RISK",
            "level": "ok" if pnl >= 0 else "warn",
            "symbol": pos["symbol"],
            "msg": f"EXIT ${pos['symbol']} ({why}) {gain_pct:+.1f}% ({pnl:+.2f}$)",
        })

    # Daily drawdown circuit breaker
    eq = equity_of(book)
    day_start = float(book.get("day_start_equity") or config.EQUITY_START_USD)
    if day_start > 0:
        day_pct = ((eq - day_start) / day_start) * 100.0
        if day_pct <= config.DAY_STOP_PCT and not config.STOP_FILE.exists():
            config.DATA_DIR.mkdir(parents=True, exist_ok=True)
            config.STOP_FILE.write_text(
                f"Daily drawdown {day_pct:.2f}% breached {config.DAY_STOP_PCT}%\n",
                encoding="utf-8",
            )
            events.append({
                "agent": "RISK",
                "level": "err",
                "symbol": "DESK",
                "msg": f"CIRCUIT BREAKER: day {day_pct:.1f}% <= {config.DAY_STOP_PCT:.0f}% — STOP file written",
            })

    save_book(book)
    return events
