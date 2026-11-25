#!/usr/bin/env python3
"""Chief of Staff desk runner — runs the 6 agents in strict order:
Scout -> Filter -> Analyst -> Executor -> Risk -> Logger.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import threading
import time
from pathlib import Path
from typing import Any

# Ensure `desk/` is on sys.path when executed from repo root or desk/
DESK_DIR = Path(__file__).resolve().parent
if str(DESK_DIR) not in sys.path:
    sys.path.insert(0, str(DESK_DIR))

import config  # noqa: E402
from agents import analyst, executor, filter_agent, logger_agent, risk, scout, wallet  # noqa: E402

_PASS_LOCK = threading.Lock()


def _load_seen() -> set[str]:
    if not config.SEEN_FILE.exists():
        return set()
    return {line.strip() for line in config.SEEN_FILE.read_text(encoding="utf-8").splitlines() if line.strip()}


def _mark_seen(mints: list[str]) -> None:
    if not mints:
        return
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    with config.SEEN_FILE.open("a", encoding="utf-8") as fh:
        for m in mints:
            fh.write(m + "\n")


def _push_log(state: dict[str, Any], agent: str, level: str, symbol: str, msg: str) -> None:
    rec = logger_agent.append_log({"agent": agent, "level": level, "symbol": symbol, "msg": msg})
    logs = state.setdefault("logs", [])
    logs.insert(0, rec)
    if len(logs) > 60:
        del logs[60:]


def _push_ticker(state: dict[str, Any], symbol: str, price: float, chg: float) -> None:
    strip = [t for t in state.get("ticker", []) if t.get("symbol") != symbol]
    strip.insert(0, {"symbol": symbol, "price": round(price, 6), "chg": round(chg, 1)})
    state["ticker"] = strip[:10]


def reset_desk(mode: str = "demo") -> dict[str, Any]:
    with _PASS_LOCK:
        for f in (config.SIGNALS_FILE, config.LOG_FILE, config.BOOK_FILE, config.STATE_FILE, config.SEEN_FILE, config.STOP_FILE):
            if f.exists():
                f.unlink()
        state = logger_agent.default_state()
        state["mode"] = mode
        logger_agent.save_state(state)
        return state


def reconcile_live_positions(book: dict[str, Any], state: dict[str, Any]) -> None:
    """Sinkronkan posisi live di `book.json` dengan saldo SPL token riil di wallet."""
    live_mints = [m for m, p in book.get("open", {}).items() if p.get("execution") == "live"]
    if not live_mints:
        return
    armed, _ = config.is_live_execution_armed()
    if not armed:
        return
    try:
        pub = wallet.wallet_pubkey()
    except Exception:
        return

    changed = False
    for mint in live_mints:
        pos = book["open"].get(mint)
        if not pos:
            continue
        try:
            raw_bal, _ = wallet.get_token_balance_raw(pub, mint)
        except Exception:
            continue
        if raw_bal <= 0:
            sym = pos.get("symbol", mint[:6])
            book["open"].pop(mint, None)
            _push_log(state, "RISK", "warn", sym, f"RECONCILE ${sym}: saldo on-chain 0 (eksternal exit), dihapus dari book")
            changed = True
        elif int(pos.get("token_amount_raw") or 0) != raw_bal:
            pos["token_amount_raw"] = raw_bal
            changed = True
    if changed:
        executor.save_book(book)


def doctor_report() -> dict[str, Any]:
    """Cek kesiapan portabel untuk Tahap 1 (paper), Tahap 2 (simulate), & Tahap 3 (live)."""
    armed, guard_msg = config.is_live_execution_armed()
    rpc_ok = bool(os.environ.get("SOLANA_RPC_URL", config.SOLANA_RPC_URL).strip())
    solders_ok = wallet.has_solders()
    key_ok = bool(os.environ.get("SOLANA_PRIVATE_KEY", "").strip())
    return {
        "env_files_loaded": list(config.LOADED_ENV_FILES),
        "stage1_paper_live_feed": True,
        "stage2_rpc_configured": rpc_ok,
        "stage3_solders_installed": solders_ok,
        "stage3_private_key_set": key_ok,
        "stage3_live_armed": armed,
        "stage3_guard_detail": guard_msg,
        "paper": config.PAPER,
        "max_live_trade_usd": config.MAX_LIVE_TRADE_USD,
        "slippage_bps": config.SLIPPAGE_BPS,
    }


def run_pass(mode: str = "demo", use_grok: bool = False) -> dict[str, Any]:
    with _PASS_LOCK:
        config.DATA_DIR.mkdir(parents=True, exist_ok=True)
        state = logger_agent.load_state()
        state["mode"] = mode
        book = executor.load_book()
        reconcile_live_positions(book, state)
        seen = _load_seen()

        # 1. SCOUT
        if mode == "live":
            candidates, err = scout.scan_live(limit=10)
            if err:
                state["agents"]["scout"] = {"status": "warn", "last": err, "badge": f"{state['scanned']} pairs"}
                _push_log(state, "SCOUT", "warn", "RPC", err)
            else:
                state["agents"]["scout"] = {
                    "status": "ok",
                    "last": "Pump.fun + Raydium (DexScreener)",
                    "badge": f"{state['scanned'] + len(candidates)} pairs",
                }
        else:
            candidates = scout.scan_demo(count=6)
            state["agents"]["scout"] = {
                "status": "ok",
                "last": "Pump.fun + Raydium (demo feed)",
                "badge": f"{state['scanned'] + len(candidates)} pairs",
            }

        scout.append_signals(candidates)
        fresh = [c for c in candidates if c["mint"] not in seen]
        state["scanned"] = int(state.get("scanned", 0)) + len(fresh)
        newly_seen: list[str] = []

        # 2. FILTER & 3. ANALYST & 4. EXECUTOR
        last_filter = state["agents"]["filter"]["last"]
        last_analyst = state["agents"]["analyst"]["last"]
        last_exec = state["agents"]["executor"]["last"]

        for sig in fresh:
            mint = sig["mint"]
            sym = sig.get("symbol") or mint[:6]
            newly_seen.append(mint)

            if mode == "demo":
                facts = sig.get("demo_facts")
            else:
                try:
                    facts = filter_agent.fetch_live_facts(mint)
                    state["filter_error_streak"] = 0
                except filter_agent.FilterDataError as exc:
                    state["filter_error_streak"] = int(state.get("filter_error_streak", 0)) + 1
                    state["killed_total"] = int(state.get("killed_total", 0)) + 1
                    state["killed_by_rule"]["Missing/error data"] = (
                        int(state["killed_by_rule"].get("Missing/error data", 0)) + 1
                    )
                    last_filter = f"${sym} ✗ error data"
                    _push_log(state, "FILTER", "err", sym, f"killed ${sym} · {exc}")
                    if state["filter_error_streak"] >= config.FILTER_ERROR_PAUSE_STREAK and not config.STOP_FILE.exists():
                        config.STOP_FILE.write_text("Paused: consecutive Filter API errors\n", encoding="utf-8")
                        _push_log(state, "RISK", "err", "DESK", "Filter API error streak -> STOP written")
                    continue

            if facts and facts.get("price_usd"):
                _push_ticker(state, str(facts.get("symbol", sym)), float(facts["price_usd"]), float(facts.get("h1_change_pct", 0)))

            passed, rule, detail = filter_agent.evaluate(facts)
            if not passed:
                state["killed_total"] = int(state.get("killed_total", 0)) + 1
                if rule:
                    state["killed_by_rule"][rule] = int(state["killed_by_rule"].get(rule, 0)) + 1
                last_filter = f"${sym} ✗ {rule}"
                _push_log(state, "FILTER", "err", sym, f"killed ${sym} · {detail}")
                continue

            last_filter = f"${sym} ✓ passed 8 rules"
            _push_log(state, "FILTER", "ok", sym, f"passed ${sym} · {detail}")

            # 3. ANALYST (score 70+ only)
            a_ok, score, a_summary = analyst.score_token(sig, facts, use_grok=use_grok)
            if not a_ok:
                last_analyst = f"${sym} ✗ score {score} < {config.MIN_ANALYST_SCORE}"
                _push_log(state, "ANALYST", "warn", sym, f"rejected ${sym} · {a_summary}")
                continue

            state["scored_pass"] = int(state.get("scored_pass", 0)) + 1
            last_analyst = f"${sym} ✓ score {score}"
            _push_log(state, "ANALYST", "ok", sym, f"approved ${sym} · {a_summary}")

            # 4. EXECUTOR (8% max size, paper mode)
            entered, exec_msg, _ = executor.execute_entry(book, mint, facts, score, mode=mode)
            last_exec = exec_msg
            _push_log(state, "EXECUTOR", "ok" if entered else "warn", sym, exec_msg)

        _mark_seen(newly_seen)

        # 5. RISK (monitors open positions even when STOP file pauses new buys)
        risk_events = risk.monitor_positions(book, mode=mode)
        for ev in risk_events:
            _push_log(state, ev["agent"], ev["level"], ev["symbol"], ev["msg"])

        # 6. LOGGER (sync metrics + save state.json)
        logger_agent.sync_book_metrics(state, book)
        state["agents"]["filter"] = {
            "status": "ok",
            "last": last_filter,
            "badge": f"{state['killed_total']} killed",
        }
        state["agents"]["analyst"] = {
            "status": "ok",
            "last": last_analyst,
            "badge": f"{state['scored_pass']} scored",
        }
        state["agents"]["executor"] = {
            "status": "warn" if state["stopped"] else "ok",
            "last": "PAUSED (STOP active)" if state["stopped"] else last_exec,
            "badge": f"size {int(config.MAX_SIZE_PCT * 100)}%",
        }
        open_n = len(state["open_positions"])
        risk_last = f"{open_n} open · {risk_events[-1]['msg']}" if risk_events else f"{open_n} open · watching stops"
        state["agents"]["risk"] = {
            "status": "err" if state["stopped"] else "ok",
            "last": risk_last,
            "badge": f"{config.STOP_PCT:.0f}% · trail +12%",
        }
        log_count = len(state.get("logs", []))
        state["agents"]["logger"] = {
            "status": "ok",
            "last": state["logs"][0]["msg"] if log_count else "reason for all",
            "badge": f"{log_count} recent",
        }
        logger_agent.save_state(state)
        return state


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenMausBot 6-Agent Trading Desk")
    parser.add_argument("--once", action="store_true", help="run a single pass and exit")
    parser.add_argument("--loop", action="store_true", help="run continuously")
    parser.add_argument("--interval", type=float, default=8.0, help="seconds between passes in --loop")
    parser.add_argument("--mode", choices=("demo", "live"), default="demo", help="data feed mode")
    parser.add_argument("--grok", action="store_true", help="enable xAI X-search scoring when XAI_API_KEY is set")
    parser.add_argument("--stop", action="store_true", help="write STOP file to pause new entries")
    parser.add_argument("--resume", action="store_true", help="remove STOP file")
    parser.add_argument("--reset", action="store_true", help="reset paper book and logs")
    parser.add_argument("--doctor", action="store_true", help="cek kesiapan Tahap 1, Tahap 2 (simulate), & Tahap 3 (live)")
    parser.add_argument("--simulate", metavar="MINT", help="Tahap 2: dry-run Jupiter swap + Solana simulateTransaction untuk token MINT")
    parser.add_argument("--usd", type=float, default=2.0, help="ukuran USD untuk --simulate (default $2.0)")
    parser.add_argument("--pubkey", default="", help="pubkey Solana opsional untuk --simulate (atau baca SOLANA_PUBLIC_KEY)")
    args = parser.parse_args()

    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    if args.doctor:
        print(json.dumps(doctor_report(), indent=2))
        return 0
    if args.simulate:
        pub = args.pubkey.strip() or os.environ.get("SOLANA_PUBLIC_KEY", "").strip()
        if not pub:
            try:
                pub = wallet.wallet_pubkey()
            except Exception as exc:
                print(f"Error: sertakan --pubkey atau SOLANA_PUBLIC_KEY ({exc})")
                return 2
        res = wallet.dry_run_swap_plan(config.USDC_MINT, args.simulate.strip(), int(max(1.0, args.usd) * 1_000_000), pub)
        print(json.dumps(res, indent=2))
        return 0 if res.get("ok") else 1
    if args.stop:
        config.STOP_FILE.write_text("Manual STOP\n", encoding="utf-8")
        print("STOP file written: new entries paused.")
        return 0
    if args.resume:
        if config.STOP_FILE.exists():
            config.STOP_FILE.unlink()
        print("STOP file removed: desk resumed.")
        return 0
    if args.reset:
        reset_desk(mode=args.mode)
        print("Desk state and paper book reset.")
        return 0

    if args.loop:
        print(f"Starting 6-agent desk loop (mode={args.mode}, interval={args.interval}s)...")
        try:
            while True:
                st = run_pass(mode=args.mode, use_grok=args.grok)
                print(
                    f"[{time.strftime('%H:%M:%S')}] eq=${st['equity_usd']:.2f} "
                    f"scanned={st['scanned']} killed={st['killed_total']} open={len(st['open_positions'])}"
                )
                time.sleep(max(1.0, args.interval))
        except KeyboardInterrupt:
            print("\nStopped.")
            return 0

    st = run_pass(mode=args.mode, use_grok=args.grok)
    print(
        f"PASS ok · mode={st['mode']} · eq=${st['equity_usd']:.2f} "
        f"({st['pnl_usd']:+.2f}$) · scanned={st['scanned']} · killed={st['killed_total']} · open={len(st['open_positions'])}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
