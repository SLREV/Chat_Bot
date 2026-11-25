"""Agent 3: Analyst — scores filtered survivors (score 70+ only)."""

from __future__ import annotations

import datetime
import json
import os
from typing import Any

import config
from agents import post_json

GROK_PROMPT = """You are the ANALYST on a 6-agent Solana trading desk.
Token ${symbol} (mint {mint}) already passed 8 hard safety checks.
Search X for this exact mint and ticker from the last 48 hours and score
community authenticity from -15 (fake/raid) to +15 (organic traction).
Return JSON only: {{"delta": -15..15, "note": "<=12 words"}}."""


def _grok_adjustment(symbol: str, mint: str) -> tuple[int, str]:
    api_key = os.environ.get("XAI_API_KEY", "").strip()
    if not api_key:
        return 0, "quant only (no XAI_API_KEY)"
    since = (datetime.date.today() - datetime.timedelta(days=2)).isoformat()
    try:
        res = post_json(
            "https://api.x.ai/v1/responses",
            {
                "model": config.MODEL,
                "max_turns": 2,
                "input": GROK_PROMPT.format(symbol=symbol, mint=mint),
                "tools": [{"type": "x_search", "from_date": since}],
            },
            timeout=25.0,
            headers={"Authorization": f"Bearer {api_key}"},
        )
        msg = [o for o in res.get("output", []) if o.get("type") == "message"][-1]
        text = msg["content"][0]["text"]
        parsed = json.loads(text[text.find("{"): text.rfind("}") + 1])
        delta = max(-15, min(15, int(parsed.get("delta", 0))))
        return delta, str(parsed.get("note", "grok x_search"))[:60]
    except Exception as exc:
        # Never boost score on Grok failure
        return 0, f"grok skipped ({type(exc).__name__})"


def score_token(sig: dict[str, Any], facts: dict[str, Any], use_grok: bool = False) -> tuple[bool, int, str]:
    """Compute a 0-100 quality score; only >= 70 passes to Executor."""
    liq = float(facts.get("liq_usd") or 0)
    vol = float(facts.get("volume_h1_usd") or 0)
    top10 = float(facts.get("top10_pct") or 25)
    dev = float(facts.get("dev_pct") or 3)
    bundled = float(facts.get("bundled_pct") or 10)
    h1 = float(facts.get("h1_change_pct") or 0)
    holders = int(facts.get("holders") or 0)

    # Quantitative score out of 100
    score = 35
    if liq >= 120_000:
        score += 20
    elif liq >= 70_000:
        score += 14
    elif liq >= 40_000:
        score += 8

    turnover = vol / liq if liq > 0 else 0
    if 0.8 <= turnover <= 4.5:
        score += 16
    elif turnover > 0.3:
        score += 9

    if top10 <= 16:
        score += 14
    elif top10 <= 21:
        score += 9
    else:
        score += 4

    if dev <= 1.0 and bundled <= 3.0:
        score += 10
    elif dev <= 2.0 and bundled <= 6.0:
        score += 6

    if 5.0 <= h1 <= 45.0:
        score += 5
    if holders >= 500:
        score += 5

    note = "quant"
    if use_grok:
        delta, note = _grok_adjustment(str(facts.get("symbol", sig.get("symbol", ""))), str(sig.get("mint", "")))
        score += delta

    score = max(0, min(100, int(round(score))))
    passed = score >= config.MIN_ANALYST_SCORE
    summary = f"score {score}/100 ({note})"
    return passed, score, summary
