"""Central configuration for the 6-agent lightweight trading desk."""

from __future__ import annotations

import os
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent
DATA_DIR = Path(os.environ.get("DESK_DATA_DIR", str(BASE_DIR / "data")))

# Mode & execution safety
PAPER = True  # Paper mode first; never places real on-chain orders here.
MODEL = os.environ.get("DESK_GROK_MODEL", "grok-4.7")

# Capital & sizing (matching strict RM desk rules)
EQUITY_START_USD = 150.0
MAX_SIZE_PCT = 0.08          # 8% max equity per position
MAX_OPEN_POSITIONS = 3
MAX_TOKEN_EXPOSURE_PCT = 0.08

# Risk & exit rules
STOP_PCT = -6.0              # -6% hard stop
# (peak_gain_pct, stop_floor_pct): once peak >= +12%, stop trails at +12% -> +35% -> +80%
TRAIL_LEVELS = [
    (120.0, 80.0),
    (60.0, 35.0),
    (25.0, 12.0),
    (12.0, 0.0),
]
TAKE_HALF_AT_PCT = 100.0
MAX_HOLD_HOURS = 4.0
DAY_STOP_PCT = -15.0         # -15% daily drawdown -> write STOP file & halt entries
FILTER_ERROR_PAUSE_STREAK = 3

# 8 hard kill rules (Filter)
FILTER_RULES = {
    "min_liq_usd": 40_000.0,         # 1) liq < $40k
    "require_mint_revoked": True,    # 2) Mint/freeze not revoked
    "require_freeze_revoked": True,
    "require_lp_safe": True,         # 3) LP not burned/locked
    "top10_max_pct": 25.0,           # 4) Top 10 holders > 25%
    "dev_max_pct": 3.0,              # 5) Dev > 3%
    "bundled_max_pct": 10.0,         # 6) Bundled > 10%
    "max_h1_change_pct": 80.0,       # 7) Already +80% in 1h
    # 8) Missing / empty / error provider data -> always killed (fail-closed)
}

# Analyst gate
MIN_ANALYST_SCORE = 70               # score 70+ only

# Data endpoints (used in --mode live)
DEXSCREENER_BOOSTS_URL = "https://api.dexscreener.com/token-boosts/latest/v1"
DEXSCREENER_PAIRS_URL = "https://api.dexscreener.com/latest/dex/tokens/{mint}"
RUGCHECK_REPORT_URL = "https://api.rugcheck.xyz/v1/tokens/{mint}/report/summary"
JUPITER_QUOTE_URL = "https://lite-api.jup.ag/swap/v1/quote"
SOL_MINT = "So11111111111111111111111111111111111111112"
USDC_MINT = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"

# File paths
SIGNALS_FILE = DATA_DIR / "signals.jsonl"
LOG_FILE = DATA_DIR / "desk_log.jsonl"
BOOK_FILE = DATA_DIR / "book.json"
STATE_FILE = DATA_DIR / "state.json"
SEEN_FILE = DATA_DIR / "seen.txt"
STOP_FILE = DATA_DIR / "STOP"
HEALTH_FILE = DATA_DIR / "health.json"
