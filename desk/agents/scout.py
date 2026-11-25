"""Agent 1: Scout — discovers fresh Solana pairs (Pump.fun + Raydium)."""

from __future__ import annotations

import json
import random
import time
from typing import Any

import config
from agents import fetch_json

_DEMO_TICKERS = [
    "TURBO2", "HAMSTER", "SOLDOG", "YOLO", "FROGMAN", "MICRODUCK",
    "JEANPHIL", "CATWIF", "MEOWFI", "ORBIT", "BORK", "PUMPZ",
    "PEPE99", "NEKO", "MOCHI", "ZEST", "CLOVER", "RAVEN",
]


def _synthetic_mint(symbol: str, seq: int) -> str:
    base = f"{symbol}{seq:04d}PumpFunRaydium"
    return (base + "11111111111111111111111111111111")[:44]


def _clean_symbol(item: dict[str, Any], mint: str) -> str:
    """Ambil ticker yang valid; abaikan URL gambar DexScreener seperti https://cdn.dexs."""
    for key in ("symbol", "tokenSymbol", "ticker"):
        val = str(item.get(key) or "").strip().lstrip("$")
        if val and "://" not in val and "/" not in val and len(val) <= 16:
            return val
    desc = str(item.get("description") or "")
    for tok in desc.split():
        if tok.startswith("$") and len(tok) > 1:
            cand = tok[1:].strip(".,:;!?()[]{}\"'")
            if cand and "://" not in cand and "/" not in cand and len(cand) <= 16:
                return cand
    return mint[:6]


def scan_live(limit: int = 12) -> tuple[list[dict[str, Any]], str | None]:
    """Fetch fresh Solana token candidates from DexScreener boosts."""
    try:
        data = fetch_json(config.DEXSCREENER_BOOSTS_URL, timeout=8.0)
    except Exception as exc:
        return [], f"scout fetch error: {type(exc).__name__}: {exc}"

    if not isinstance(data, list):
        return [], "scout invalid response shape"

    now = time.time()
    seen_mints: set[str] = set()
    out: list[dict[str, Any]] = []
    for item in data:
        if len(out) >= limit:
            break
        if str(item.get("chainId", "")).lower() != "solana":
            continue
        mint = str(item.get("tokenAddress") or "").strip()
        if not mint or mint in seen_mints:
            continue
        seen_mints.add(mint)
        out.append({
            "mint": mint,
            "symbol": _clean_symbol(item, mint),
            "dex": "Pump.fun + Raydium",
            "source": "dexscreener",
            "ts": now,
        })
    return out, None


def scan_demo(count: int = 6, rng: random.Random | None = None) -> list[dict[str, Any]]:
    """Generate realistic Pump.fun + Raydium candidates for offline paper testing."""
    r = rng or random
    now = time.time()
    picks = r.sample(_DEMO_TICKERS, k=min(count, len(_DEMO_TICKERS)))
    out: list[dict[str, Any]] = []
    for idx, sym in enumerate(picks):
        # Mix mostly risky tokens (matching real memecoin survival rates) with
        # occasional clean setups so all 6 agents stay exercised.
        roll = r.random()
        if roll < 0.22:
            # Liquidity failure (< $40k)
            liq = r.uniform(6_000, 38_000)
            top10 = r.uniform(14, 24)
            dev = r.uniform(0.2, 2.5)
            bundled = r.uniform(0, 6)
            h1 = r.uniform(5, 65)
            mint_rev, freeze_rev, lp_safe = True, True, True
        elif roll < 0.42:
            # Top 10 concentration failure (> 25%)
            liq = r.uniform(42_000, 180_000)
            top10 = r.uniform(26, 48)
            dev = r.uniform(0.5, 2.8)
            bundled = r.uniform(0, 8)
            h1 = r.uniform(5, 65)
            mint_rev, freeze_rev, lp_safe = True, True, True
        elif roll < 0.56:
            # LP not burned/locked
            liq = r.uniform(45_000, 120_000)
            top10 = r.uniform(12, 23)
            dev = r.uniform(0.4, 2.5)
            bundled = r.uniform(0, 7)
            h1 = r.uniform(5, 55)
            mint_rev, freeze_rev, lp_safe = True, True, False
        elif roll < 0.68:
            # Mint/freeze not revoked
            liq = r.uniform(45_000, 140_000)
            top10 = r.uniform(12, 23)
            dev = r.uniform(0.4, 2.5)
            bundled = r.uniform(0, 7)
            h1 = r.uniform(5, 55)
            mint_rev, freeze_rev, lp_safe = False, True, True
        elif roll < 0.78:
            # Dev / bundled / already pumped
            liq = r.uniform(50_000, 160_000)
            top10 = r.uniform(12, 23)
            dev = r.uniform(3.2, 9.0) if r.random() < 0.5 else r.uniform(0.5, 2.5)
            bundled = r.uniform(11.0, 26.0) if dev <= 3.0 else r.uniform(1, 8)
            h1 = r.uniform(82.0, 190.0) if (dev <= 3.0 and bundled <= 10.0) else r.uniform(10, 60)
            mint_rev, freeze_rev, lp_safe = True, True, True
        else:
            # Clean token that passes all 8 rules -> reaches Analyst
            liq = r.uniform(48_000, 260_000)
            top10 = r.uniform(11, 23)
            dev = r.uniform(0.2, 2.4)
            bundled = r.uniform(0, 7.5)
            h1 = r.uniform(4, 58)
            mint_rev, freeze_rev, lp_safe = True, True, True

        seq = int(now) % 10_000 + idx
        price = round(r.uniform(0.00018, 0.0145), 6)
        vol_h1 = round(liq * r.uniform(0.6, 3.8), 2)
        out.append({
            "mint": _synthetic_mint(sym, seq),
            "symbol": sym,
            "dex": r.choice(["Pump.fun", "Raydium"]),
            "source": "demo",
            "ts": now,
            "demo_facts": {
                "symbol": sym,
                "liq_usd": round(liq, 2),
                "price_usd": price,
                "volume_h1_usd": vol_h1,
                "h1_change_pct": round(h1, 2),
                "top10_pct": round(top10, 2),
                "dev_pct": round(dev, 2),
                "bundled_pct": round(bundled, 2),
                "mint_revoked": mint_rev,
                "freeze_revoked": freeze_rev,
                "lp_safe": lp_safe,
                "pool_age_min": round(r.uniform(18, 240), 1),
                "holders": r.randint(140, 2400),
            },
        })
    return out


def append_signals(signals: list[dict[str, Any]]) -> None:
    if not signals:
        return
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    with config.SIGNALS_FILE.open("a", encoding="utf-8") as fh:
        for sig in signals:
            fh.write(json.dumps(sig) + "\n")
