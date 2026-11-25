"""Agent 4: Executor — sizing (8% max), paper fills (Tahap 1), dan live swap (Tahap 3)."""

from __future__ import annotations

import json
import os
import time
from typing import Any

import config
from agents import fetch_json, wallet


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
    """Tahap 1: quote Jupiter read-only untuk validasi likuiditas & routing sebelum beli."""
    try:
        amount_usdc = max(1_000_000, int(usd_size * 1_000_000))
        url = (
            f"{config.JUPITER_QUOTE_URL}?inputMint={config.USDC_MINT}"
            f"&outputMint={mint}&amount={amount_usdc}&slippageBps={config.SLIPPAGE_BPS}"
        )
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
        return False, "insufficient cash"
    return True, f"size ${size_usd:.2f} (8% eq)"


def _execute_live_buy(
    book: dict[str, Any],
    mint: str,
    facts: dict[str, Any],
    score: int,
    size_usd: float,
    entry_price: float,
) -> tuple[bool, str, dict[str, Any] | None]:
    """Tahap 3: eksekusi BUY sungguhan ke Jupiter Swap + Solana RPC (dengan cap burner wallet)."""
    # Batasi ukuran transaksi live sesuai cap burner wallet (default $5)
    capped_usd = round(min(size_usd, config.MAX_LIVE_TRADE_USD), 2)
    if capped_usd < 1.0:
        return False, "live trade size < $1.00", None

    try:
        pub = wallet.wallet_pubkey()
        sol_bal = wallet.get_sol_balance(pub)
        if sol_bal < config.MIN_SOL_FOR_GAS:
            return False, f"SOL gas rendah ({sol_bal:.4f} < {config.MIN_SOL_FOR_GAS})", None

        usdc_bal = wallet.get_usdc_balance(pub)
        if usdc_bal < capped_usd:
            return False, f"USDC on-chain tidak cukup (${usdc_bal:.2f} < ${capped_usd:.2f})", None

        pre_raw, decimals = wallet.get_token_balance_raw(pub, mint)
    except Exception as exc:
        return False, f"live wallet check gagal: {exc}", None

    amount_usdc_raw = int(round(capped_usd * 1_000_000))
    swap = wallet.execute_swap(config.USDC_MINT, mint, amount_usdc_raw, dry_run=False)
    if not swap.get("ok") or not swap.get("tx_sig"):
        return False, f"live BUY gagal: {swap.get('err')}", None

    # Baca saldo token aktual sesudah transaksi terkonfirmasi agar jumlah unit akurat untuk EXIT
    try:
        post_raw, decimals = wallet.get_token_balance_raw(pub, mint)
        received_raw = max(0, post_raw - pre_raw)
    except Exception:
        received_raw = 0
    if received_raw <= 0:
        received_raw = int(swap.get("out_amount_raw") or 0)

    now = time.time()
    pos = {
        "mint": mint,
        "symbol": str(facts.get("symbol") or mint[:6]),
        "entry_price": entry_price,
        "last_price": entry_price,
        "usd": capped_usd,
        "current_usd": capped_usd,
        "peak_pct": 0.0,
        "stop_pct": config.STOP_PCT,
        "half_taken": False,
        "score": score,
        "opened_at": now,
        "quote": "jupiter live",
        "execution": "live",
        "tx_sig": swap["tx_sig"],
        "token_amount_raw": received_raw,
        "token_decimals": decimals,
        "exit_failures": 0,
    }
    book["cash"] = round(max(0.0, usdc_bal - capped_usd), 4)
    book["open"][mint] = pos
    book["trades_total"] = int(book.get("trades_total", 0)) + 1
    save_book(book)
    return True, f"LIVE BUY ${pos['symbol']} ${capped_usd:.2f} tx={swap['tx_sig'][:10]}…", pos


def execute_entry(
    book: dict[str, Any],
    mint: str,
    facts: dict[str, Any],
    score: int,
    mode: str = "demo",
) -> tuple[bool, str, dict[str, Any] | None]:
    """Buka posisi baru: paper mode (Tahap 1) atau live on-chain jika double-lock aktif (Tahap 3)."""
    ok, reason = can_enter(book, mint)
    if not ok:
        return False, reason, None

    eq = equity_of(book)
    size_usd = round(min(eq * config.MAX_SIZE_PCT, float(book["cash"])), 2)
    entry_price = float(facts.get("price_usd") or 0)
    if entry_price <= 0:
        return False, "invalid entry price", None

    # Double-lock check: jika PAPER=False, wajib lolos DESK_LIVE_CONFIRM + RPC + Private Key
    if not config.PAPER:
        armed, guard_msg = config.is_live_execution_armed()
        if not armed:
            return False, f"live execution blocked: {guard_msg}", None
        return _execute_live_buy(book, mint, facts, score, size_usd, entry_price)

    # Tahap 1 (PAPER = True): validasi quote Jupiter saat mode == "live", tetapi tetap paper fill
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
        "execution": "paper",
    }
    book["cash"] = round(float(book["cash"]) - size_usd, 4)
    book["open"][mint] = pos
    book["trades_total"] = int(book.get("trades_total", 0)) + 1
    save_book(book)
    return True, f"BUY ${pos['symbol']} ${size_usd:.2f} @ ${entry_price:.6f} ({quote_note})", pos
