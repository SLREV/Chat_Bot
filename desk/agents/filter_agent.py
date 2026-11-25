"""Agent 2: Filter — deterministic 8-rule fail-closed safety gate."""

from __future__ import annotations

from typing import Any

import config
from agents import fetch_json

RULE_LABELS = {
    "liq < $40k": "liq < $40k",
    "Mint/freeze not revoked": "Mint/freeze not revoked",
    "LP not burned/locked": "LP not burned/locked",
    "Top 10 holders > 25%": "Top 10 holders > 25%",
    "Dev > 3%": "Dev > 3%",
    "Bundled > 10%": "Bundled > 10%",
    "Already +80% in 1h": "Already +80% in 1h",
    "Missing/error data": "Missing/error data",
}


class FilterDataError(Exception):
    """Raised when upstream Rugcheck/DexScreener data is unavailable."""


def fetch_live_facts(mint: str) -> dict[str, Any] | None:
    """Fetch Rugcheck + DexScreener facts for a live Solana mint.

    Returns None when data is incomplete (fail-closed) and raises FilterDataError
    on HTTP/network errors so the crew can track consecutive provider failures.
    """
    try:
        dex = fetch_json(config.DEXSCREENER_PAIRS_URL.format(mint=mint), timeout=8.0)
        rug = fetch_json(config.RUGCHECK_REPORT_URL.format(mint=mint), timeout=8.0)
    except Exception as exc:
        raise FilterDataError(f"{type(exc).__name__}: {exc}") from exc

    pairs = dex.get("pairs") if isinstance(dex, dict) else None
    if not pairs or not isinstance(rug, dict) or not rug:
        return None

    sol_pairs = [p for p in pairs if str(p.get("chainId", "")).lower() == "solana"]
    if not sol_pairs:
        return None
    best = max(sol_pairs, key=lambda p: float((p.get("liquidity") or {}).get("usd") or 0))

    liq_usd = float((best.get("liquidity") or {}).get("usd") or 0)
    price_usd = float(best.get("priceUsd") or 0)
    vol_h1 = float((best.get("volume") or {}).get("h1") or 0)
    h1_change = float((best.get("priceChange") or {}).get("h1") or 0)
    symbol = str((best.get("baseToken") or {}).get("symbol") or mint[:6]).strip()

    # Rugcheck summary fields
    risks = rug.get("risks") or []
    risk_names = " ".join(str(r.get("name", "")).lower() for r in risks if isinstance(r, dict))

    mint_revoked = rug.get("mintAuthority") in (None, "", "null") and "mint authority" not in risk_names
    freeze_revoked = rug.get("freezeAuthority") in (None, "", "null") and "freeze authority" not in risk_names

    lp_locked_pct = float(rug.get("lpLockedPct") or 0)
    lp_safe = lp_locked_pct >= 90.0 or ("lp" not in risk_names and "unlocked" not in risk_names and lp_locked_pct > 0)

    top_holders = rug.get("topHolders") or []
    top10_pct = sum(
        float(h.get("pct") or 0)
        for h in top_holders[:10]
        if isinstance(h, dict) and not h.get("insider")
    )
    if top10_pct == 0 and isinstance(rug.get("top10Pct"), (int, float)):
        top10_pct = float(rug["top10Pct"])

    dev_pct = float(rug.get("creatorBalancePct") or 0)
    insiders = rug.get("insiderNetworks") or []
    bundled_pct = float(rug.get("bundledPct") or (len(insiders) * 5.0))

    return {
        "symbol": symbol,
        "liq_usd": liq_usd,
        "price_usd": price_usd,
        "volume_h1_usd": vol_h1,
        "h1_change_pct": h1_change,
        "top10_pct": round(top10_pct, 2),
        "dev_pct": round(dev_pct, 2),
        "bundled_pct": round(bundled_pct, 2),
        "mint_revoked": mint_revoked,
        "freeze_revoked": freeze_revoked,
        "lp_safe": lp_safe,
        "pool_age_min": 30.0,
        "holders": int(rug.get("totalHolders") or 0),
    }


def evaluate(facts: dict[str, Any] | None) -> tuple[bool, str | None, str]:
    """Check all 8 kill rules. Returns (passed, rule_key, detail_line)."""
    if not facts:
        return False, "Missing/error data", "missing or empty report"

    rules = config.FILTER_RULES
    required = ("liq_usd", "top10_pct", "dev_pct", "bundled_pct", "h1_change_pct", "mint_revoked", "freeze_revoked", "lp_safe")
    if any(facts.get(k) is None for k in required):
        return False, "Missing/error data", "incomplete token fields"

    if float(facts["liq_usd"]) < rules["min_liq_usd"]:
        return False, "liq < $40k", f"liq ${float(facts['liq_usd']):,.0f} < $40k"

    if (rules["require_mint_revoked"] and not facts["mint_revoked"]) or (
        rules["require_freeze_revoked"] and not facts["freeze_revoked"]
    ):
        return False, "Mint/freeze not revoked", "mint or freeze authority still active"

    if rules["require_lp_safe"] and not facts["lp_safe"]:
        return False, "LP not burned/locked", "LP not burned/locked"

    if float(facts["top10_pct"]) > rules["top10_max_pct"]:
        return False, "Top 10 holders > 25%", f"top 10 holders {float(facts['top10_pct']):.1f}%"

    if float(facts["dev_pct"]) > rules["dev_max_pct"]:
        return False, "Dev > 3%", f"dev holds {float(facts['dev_pct']):.1f}%"

    if float(facts["bundled_pct"]) > rules["bundled_max_pct"]:
        return False, "Bundled > 10%", f"bundled {float(facts['bundled_pct']):.1f}%"

    if float(facts["h1_change_pct"]) > rules["max_h1_change_pct"]:
        return False, "Already +80% in 1h", f"1h +{float(facts['h1_change_pct']):.0f}%"

    return True, None, f"passed 8 rules (liq ${float(facts['liq_usd']):,.0f})"
