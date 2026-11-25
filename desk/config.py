"""Central configuration for the 6-agent lightweight trading desk."""

from __future__ import annotations

import os
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent
ROOT_DIR = BASE_DIR.parent


def load_dotenv_file(path: Path) -> bool:
    """Muat variabel KEY=VALUE dari file `.env` tanpa menimpa ENV yang sudah diset.

    Menggunakan Python stdlib murni (0 dependensi eksternal). Mendukung:
    - baris komentar `# ...`
    - awalan opsional `export KEY=VAL`
    - nilai diapit kutip tunggal/ganda (`KEY="..."` atau `KEY='...'`)
    """
    if not path.exists() or not path.is_file():
        return False
    for raw_line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        if "=" not in line:
            continue
        key, val = line.split("=", 1)
        key = key.strip()
        if not key or not (key[0].isalpha() or key[0] == "_") or not all(c.isalnum() or c == "_" for c in key):
            continue
        val = val.strip()
        if len(val) >= 2 and val[0] == val[-1] and val[0] in ('"', "'"):
            val = val[1:-1]
        elif " #" in val:
            val = val.split(" #", 1)[0].strip()
        if key not in os.environ:
            os.environ[key] = val
    return True


LOADED_ENV_FILES: list[str] = [
    str(p)
    for p in (BASE_DIR / ".env", ROOT_DIR / ".env")
    if load_dotenv_file(p)
]

DATA_DIR = Path(os.environ.get("DESK_DATA_DIR", str(BASE_DIR / "data")))

# ---------------------------------------------------------------------------
# Mode & safety lock bertahap (Tahap 1 -> Tahap 2 -> Tahap 3)
# ---------------------------------------------------------------------------
# Tahap 1 (--mode live, PAPER=True): data pasar & quote Jupiter live, order tetap paper.
# Tahap 3 (on-chain live): wajib set ENV:
#   DESK_EXECUTION_MODE=live
#   DESK_LIVE_CONFIRM=I_UNDERSTAND_REAL_MONEY
#   SOLANA_RPC_URL=https://...
#   SOLANA_PRIVATE_KEY=...
#   (opsional) DESK_MAX_LIVE_TRADE_USD=5.0  -> batas modal burner wallet per entry!
PAPER = os.environ.get("DESK_EXECUTION_MODE", "paper").strip().lower() != "live"
LIVE_CONFIRM_PHRASE = "I_UNDERSTAND_REAL_MONEY"
MAX_LIVE_TRADE_USD = float(os.environ.get("DESK_MAX_LIVE_TRADE_USD", "5.0"))
MIN_SOL_FOR_GAS = float(os.environ.get("DESK_MIN_SOL_FOR_GAS", "0.01"))

MODEL = os.environ.get("DESK_GROK_MODEL", "grok-4.7")
GROK_COOLDOWN_SEC = max(0.0, float(os.environ.get("DESK_GROK_COOLDOWN_SEC", "0")))
DASHBOARD_BIND = os.environ.get("DESK_BIND", "127.0.0.1")
DASHBOARD_PORT = int(os.environ.get("DESK_PORT", "8788"))

# Pengaturan transaksi Solana / Jupiter (Tahap 2 & Tahap 3)
SOLANA_RPC_URL = os.environ.get("SOLANA_RPC_URL", "")
JUPITER_SWAP_URL = os.environ.get("DESK_JUPITER_SWAP_URL", "https://lite-api.jup.ag/swap/v1/swap")
SLIPPAGE_BPS = int(os.environ.get("DESK_SLIPPAGE_BPS", "150"))
PRIORITY_FEE_LAMPORTS = int(os.environ.get("DESK_PRIORITY_FEE_LAMPORTS", "50000"))
TX_CONFIRM_TIMEOUT_SEC = float(os.environ.get("DESK_TX_CONFIRM_TIMEOUT_SEC", "45.0"))


def is_live_execution_armed() -> tuple[bool, str]:
    """Double-lock pengaman sebelum transaksi uang asli bisa dikirim ke Solana."""
    if PAPER:
        return False, "PAPER=True (mode paper aktif)"
    if os.environ.get("DESK_LIVE_CONFIRM", "").strip() != LIVE_CONFIRM_PHRASE:
        return False, f"DESK_LIVE_CONFIRM harus diisi '{LIVE_CONFIRM_PHRASE}'"
    if not os.environ.get("SOLANA_RPC_URL", SOLANA_RPC_URL).strip():
        return False, "SOLANA_RPC_URL belum diset"
    if not os.environ.get("SOLANA_PRIVATE_KEY", "").strip():
        return False, "SOLANA_PRIVATE_KEY belum diset"
    return True, "armed (live on-chain burner cap)"

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
TOKEN_FILE = DATA_DIR / ".auth_token"
