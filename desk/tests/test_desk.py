"""Unit tests for the 6-agent trading desk (Python stdlib unittest)."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

DESK_DIR = Path(__file__).resolve().parent.parent
if str(DESK_DIR) not in sys.path:
    sys.path.insert(0, str(DESK_DIR))

import config  # noqa: E402
import crew  # noqa: E402
import dashboard  # noqa: E402
from agents import analyst, executor, filter_agent, risk, wallet  # noqa: E402


def _clean_facts(**overrides):
    base = {
        "symbol": "CLEAN",
        "liq_usd": 140_000.0,
        "price_usd": 0.0042,
        "volume_h1_usd": 240_000.0,
        "h1_change_pct": 18.0,
        "top10_pct": 15.0,
        "dev_pct": 0.8,
        "bundled_pct": 2.0,
        "mint_revoked": True,
        "freeze_revoked": True,
        "lp_safe": True,
        "pool_age_min": 45.0,
        "holders": 850,
    }
    base.update(overrides)
    return base


class DeskTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        d = Path(self.tmp.name)
        config.DATA_DIR = d
        config.SIGNALS_FILE = d / "signals.jsonl"
        config.LOG_FILE = d / "desk_log.jsonl"
        config.BOOK_FILE = d / "book.json"
        config.STATE_FILE = d / "state.json"
        config.SEEN_FILE = d / "seen.txt"
        config.STOP_FILE = d / "STOP"
        config.HEALTH_FILE = d / "health.json"

    def test_filter_enforces_all_8_rules(self) -> None:
        ok, rule, _ = filter_agent.evaluate(_clean_facts())
        self.assertTrue(ok)
        self.assertIsNone(rule)

        cases = [
            (None, "Missing/error data"),
            (_clean_facts(liq_usd=None), "Missing/error data"),
            (_clean_facts(liq_usd=39_999), "liq < $40k"),
            (_clean_facts(mint_revoked=False), "Mint/freeze not revoked"),
            (_clean_facts(freeze_revoked=False), "Mint/freeze not revoked"),
            (_clean_facts(lp_safe=False), "LP not burned/locked"),
            (_clean_facts(top10_pct=25.1), "Top 10 holders > 25%"),
            (_clean_facts(dev_pct=3.1), "Dev > 3%"),
            (_clean_facts(bundled_pct=10.1), "Bundled > 10%"),
            (_clean_facts(h1_change_pct=80.5), "Already +80% in 1h"),
        ]
        for facts, want_rule in cases:
            passed, got_rule, _ = filter_agent.evaluate(facts)
            self.assertFalse(passed)
            self.assertEqual(got_rule, want_rule)

    def test_analyst_score_gate(self) -> None:
        sig = {"mint": "MintA", "symbol": "CLEAN"}
        ok, score, _ = analyst.score_token(sig, _clean_facts(), use_grok=False)
        self.assertTrue(ok)
        self.assertGreaterEqual(score, 70)

        weak = _clean_facts(liq_usd=41_000, volume_h1_usd=1_000, top10_pct=24.5, dev_pct=2.9, bundled_pct=9.5, h1_change_pct=2.0, holders=40)
        ok_w, score_w, _ = analyst.score_token(sig, weak, use_grok=False)
        self.assertFalse(ok_w)
        self.assertLess(score_w, 70)

    def test_executor_respects_8pct_size_and_stop(self) -> None:
        book = executor.load_book()
        entered, _, pos = executor.execute_entry(book, "Mint1", _clean_facts(), 82, mode="demo")
        self.assertTrue(entered)
        self.assertIsNotNone(pos)
        self.assertAlmostEqual(pos["usd"], 12.0, places=2)  # 8% of $150 = $12

        config.STOP_FILE.write_text("manual stop\n", encoding="utf-8")
        entered2, msg2, _ = executor.execute_entry(book, "Mint2", _clean_facts(), 85, mode="demo")
        self.assertFalse(entered2)
        self.assertIn("STOP", msg2)

    def test_risk_hard_stop_and_day_breaker(self) -> None:
        book = executor.load_book()
        executor.execute_entry(book, "MintStop", _clean_facts(price_usd=1.0), 80, mode="demo")
        # Force -10% drop below -6% hard stop
        book["open"]["MintStop"]["last_price"] = 0.90
        evs = risk.monitor_positions(book, mode="live")  # live skips random step if unreachable, keeps last_price=0.90
        self.assertEqual(len(book["open"]), 0)
        self.assertTrue(any("hard stop" in e["msg"] for e in evs))

        # Force -20% day drawdown
        book["cash"] = 120.0
        book["day_start_equity"] = 150.0
        evs2 = risk.monitor_positions(book, mode="live")
        self.assertTrue(config.STOP_FILE.exists())
        self.assertTrue(any("CIRCUIT BREAKER" in e["msg"] for e in evs2))

    def test_risk_max_hold_closes_even_when_trailing_stop_non_negative(self) -> None:
        book = executor.load_book()
        executor.execute_entry(book, "MintOldZero", _clean_facts(symbol="CLEAN1", price_usd=1.0), 82, mode="demo")
        executor.execute_entry(book, "MintOldPos", _clean_facts(symbol="CLEAN2", price_usd=1.0), 85, mode="demo")
        stale = time.time() - (config.MAX_HOLD_HOURS * 3600.0 + 120.0)
        # Position 1: peaked +15% (stop_pct == 0.0), sitting above stop at +5%
        book["open"]["MintOldZero"]["opened_at"] = stale
        book["open"]["MintOldZero"]["peak_pct"] = 15.0
        book["open"]["MintOldZero"]["stop_pct"] = 0.0
        book["open"]["MintOldZero"]["last_price"] = 1.05
        # Position 2: peaked +30% (stop_pct == +12.0), sitting above stop at +18%
        book["open"]["MintOldPos"]["opened_at"] = stale
        book["open"]["MintOldPos"]["peak_pct"] = 30.0
        book["open"]["MintOldPos"]["stop_pct"] = 12.0
        book["open"]["MintOldPos"]["last_price"] = 1.18

        evs = risk.monitor_positions(book, mode="live")
        self.assertEqual(len(book["open"]), 0)
        max_hold_exits = [e for e in evs if "max hold" in e["msg"]]
        self.assertEqual(len(max_hold_exits), 2)

    def test_dashboard_auth_csrf_and_default_loopback_bind(self) -> None:
        self.assertEqual(config.DASHBOARD_BIND, "127.0.0.1")

        crew.run_pass(mode="demo", use_grok=False)
        token = "test-secret-token-123"
        srv = dashboard.DeskHTTPServer(("127.0.0.1", 0), dashboard.DeskHandler, auth_token=token)
        t = threading.Thread(target=srv.serve_forever, daemon=True)
        t.start()
        self.addCleanup(srv.shutdown)
        self.addCleanup(srv.server_close)
        port = srv.server_address[1]
        base = f"http://127.0.0.1:{port}"

        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None

        opener = urllib.request.build_opener(NoRedirect())

        def req(method: str, path: str, body: bytes | None = None, headers: dict[str, str] | None = None):
            r = urllib.request.Request(base + path, data=body, method=method, headers=headers or {})
            try:
                with opener.open(r, timeout=5) as resp:
                    return resp.status, dict(resp.headers), resp.read()
            except urllib.error.HTTPError as e:
                return e.code, dict(e.headers), e.read()

        # Public health check stays minimal and ok
        st, _, data = req("GET", "/api/health")
        self.assertEqual(st, 200)
        self.assertTrue(json.loads(data)["ok"])

        # Unauthenticated requests are rejected with 401
        for m, p in [("GET", "/"), ("GET", "/api/state"), ("GET", "/api/events"), ("POST", "/api/control"), ("POST", "/api/pass"), ("POST", "/api/reset")]:
            st, _, _ = req(m, p, b"{}" if m == "POST" else None, {"Content-Type": "application/json"} if m == "POST" else None)
            self.assertEqual(st, 401, f"expected 401 for {m} {p}, got {st}")

        # Query-param token bootstrap sets HttpOnly; SameSite=Strict cookie & redirects
        st, hdrs, _ = req("GET", f"/?token={token}")
        self.assertEqual(st, 302)
        cookie_hdr = hdrs.get("Set-Cookie") or hdrs.get("set-cookie") or ""
        self.assertIn("HttpOnly", cookie_hdr)
        self.assertIn("SameSite=Strict", cookie_hdr)
        cookie_val = cookie_hdr.split(";", 1)[0]

        # Cookie allows GET /api/state
        st, _, state_raw = req("GET", "/api/state", headers={"Cookie": cookie_val})
        self.assertEqual(st, 200)
        self.assertIn("agents", json.loads(state_raw))

        # Cookie POST without X-Desk-CSRF is blocked (403)
        st, _, _ = req("POST", "/api/control", b'{"action":"stop"}', {"Cookie": cookie_val, "Content-Type": "application/json"})
        self.assertEqual(st, 403)

        # Cross-origin / Sec-Fetch-Site: cross-site POST is blocked (403)
        st, _, _ = req("POST", "/api/control", b'{"action":"stop"}', {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Origin": "https://evil.example",
        })
        self.assertEqual(st, 403)
        st, _, _ = req("POST", "/api/control", b'{"action":"stop"}', {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Sec-Fetch-Site": "cross-site",
        })
        self.assertEqual(st, 403)

        # Non-JSON Content-Type on POST is rejected (415)
        st, _, _ = req("POST", "/api/control", b"action=stop", {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/x-www-form-urlencoded",
        })
        self.assertEqual(st, 415)

        # Valid Bearer + JSON POST succeeds
        st, _, ctrl_raw = req("POST", "/api/control", b'{"action":"stop"}', {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        })
        self.assertEqual(st, 200)
        self.assertTrue(json.loads(ctrl_raw)["stopped"])

    def test_crew_demo_pass_writes_state_and_logs(self) -> None:
        st = crew.run_pass(mode="demo", use_grok=False)
        self.assertTrue(config.STATE_FILE.exists())
        self.assertTrue(config.LOG_FILE.exists())
        self.assertGreater(st["scanned"], 0)
        saved = json.loads(config.STATE_FILE.read_text(encoding="utf-8"))
        self.assertEqual(set(saved["agents"].keys()), {"scout", "filter", "analyst", "executor", "risk", "logger"})

    def test_dotenv_loader_parses_values_without_overriding_env(self) -> None:
        env_path = Path(self.tmp.name) / ".env"
        env_path.write_text(
            "# komentar\n"
            "DESK_TEST_A=hello # inline comment\n"
            'DESK_TEST_B="quoted value"\n'
            "export DESK_TEST_C='single_quoted'\n"
            "DESK_TEST_EXISTING=from_file\n",
            encoding="utf-8",
        )
        with mock.patch.dict(os.environ, {"DESK_TEST_EXISTING": "from_terminal"}, clear=False):
            for k in ("DESK_TEST_A", "DESK_TEST_B", "DESK_TEST_C"):
                os.environ.pop(k, None)
            loaded = config.load_dotenv_file(env_path)
            self.assertTrue(loaded)
            self.assertEqual(os.environ.get("DESK_TEST_A"), "hello")
            self.assertEqual(os.environ.get("DESK_TEST_B"), "quoted value")
            self.assertEqual(os.environ.get("DESK_TEST_C"), "single_quoted")
            self.assertEqual(os.environ.get("DESK_TEST_EXISTING"), "from_terminal")

    def test_stage2_dry_run_simulate_transaction_without_private_key(self) -> None:
        with mock.patch.object(wallet, "fetch_json", return_value={"inAmount": "2000000", "outAmount": "5000000", "priceImpactPct": "0.12"}):
            with mock.patch.object(
                wallet,
                "post_json",
                side_effect=[
                    {"swapTransaction": "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="},
                    {"jsonrpc": "2.0", "id": 1, "result": {"value": {"err": None, "unitsConsumed": 91234, "logs": ["Program log: OK"]}}},
                ],
            ):
                res = wallet.dry_run_swap_plan(
                    config.USDC_MINT,
                    "So11111111111111111111111111111111111111112",
                    2_000_000,
                    "11111111111111111111111111111111",
                    rpc_url="https://rpc.example",
                )
        self.assertTrue(res["ok"])
        self.assertEqual(res["out_amount_raw"], 5_000_000)
        self.assertEqual(res["simulation"]["units_consumed"], 91234)

    def test_stage3_double_lock_and_live_buy_take_half_and_exit(self) -> None:
        book = executor.load_book()
        prev_paper = config.PAPER
        prev_cap = config.MAX_LIVE_TRADE_USD
        self.addCleanup(lambda: setattr(config, "PAPER", prev_paper))
        self.addCleanup(lambda: setattr(config, "MAX_LIVE_TRADE_USD", prev_cap))

        # 1) PAPER=False tanpa DESK_LIVE_CONFIRM wajib diblokir
        config.PAPER = False
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("DESK_LIVE_CONFIRM", None)
            ok, msg, _ = executor.execute_entry(book, "MintLive1", _clean_facts(), 85, mode="live")
            self.assertFalse(ok)
            self.assertIn("DESK_LIVE_CONFIRM", msg)

        # 2) Double-lock lengkap -> BUY memakai cap burner wallet ($5) + catat tx_sig & token_amount_raw
        config.MAX_LIVE_TRADE_USD = 5.0
        env = {
            "DESK_LIVE_CONFIRM": config.LIVE_CONFIRM_PHRASE,
            "SOLANA_RPC_URL": "https://rpc.example",
            "SOLANA_PRIVATE_KEY": "dummy-key",
        }
        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(wallet, "wallet_pubkey", return_value="BurnerPub1111111111111111111111111111111111"):
                with mock.patch.object(wallet, "get_sol_balance", return_value=0.15):
                    with mock.patch.object(wallet, "get_usdc_balance", return_value=50.0):
                        with mock.patch.object(wallet, "get_token_balance_raw", side_effect=[(0, 6), (1_000_000_000, 6)]):
                            with mock.patch.object(
                                wallet,
                                "execute_swap",
                                return_value={"ok": True, "tx_sig": "5LiveBuySig1111111111111111111111111111111111", "out_amount_raw": 1_000_000_000},
                            ):
                                ok, msg, pos = executor.execute_entry(book, "MintLive1", _clean_facts(price_usd=1.0), 88, mode="live")

            self.assertTrue(ok, msg)
            self.assertIsNotNone(pos)
            self.assertEqual(pos["execution"], "live")
            self.assertEqual(pos["usd"], 5.0)
            self.assertEqual(pos["token_amount_raw"], 1_000_000_000)

            # 3) Jika exit gagal, posisi live TIDAK boleh dihapus dari book["open"]
            with mock.patch.object(risk, "_live_price", return_value=0.90):
                with mock.patch.object(wallet, "execute_swap", return_value={"ok": False, "err": "rpc timeout"}):
                    evs_fail = risk.monitor_positions(book, mode="live")
            self.assertIn("MintLive1", book["open"])
            self.assertTrue(any("LIVE EXIT GAGAL #1" in e["msg"] for e in evs_fail))

            # 4) TAKE-HALF (+100%) menjual 50% saldo token on-chain
            with mock.patch.object(risk, "_live_price", return_value=2.10):
                with mock.patch.object(
                    wallet,
                    "execute_swap",
                    return_value={"ok": True, "tx_sig": "5HalfSig111111111111111111111111111111111111", "out_amount_raw": 5_250_000},
                ) as swap_half:
                    evs_half = risk.monitor_positions(book, mode="live")
            self.assertTrue(swap_half.called)
            self.assertEqual(swap_half.call_args.args[2], 500_000_000)
            self.assertTrue(book["open"]["MintLive1"]["half_taken"])
            self.assertEqual(book["open"]["MintLive1"]["token_amount_raw"], 500_000_000)
            self.assertTrue(any("LIVE TAKE-HALF" in e["msg"] for e in evs_half))

            # 5) EXIT (trail stop / max hold) menjual 100% sisa token (500_000_000) & menutup posisi
            book["open"]["MintLive1"]["opened_at"] = time.time() - (config.MAX_HOLD_HOURS * 3600.0 + 60.0)
            with mock.patch.object(risk, "_live_price", return_value=2.05):
                with mock.patch.object(
                    wallet,
                    "execute_swap",
                    return_value={"ok": True, "tx_sig": "5ExitSig111111111111111111111111111111111111", "out_amount_raw": 5_100_000},
                ) as swap_exit:
                    evs_exit = risk.monitor_positions(book, mode="live")
            self.assertEqual(swap_exit.call_args.args[2], 500_000_000)
            self.assertEqual(len(book["open"]), 0)
            self.assertTrue(any("EXIT $CLEAN" in e["msg"] and "tx=5ExitSig11" in e["msg"] for e in evs_exit))

    def test_demo_soak_bounded_and_no_duplicate_symbols_or_frozen_trail(self) -> None:
        import random

        book = executor.load_book()
        # 1) Duplicate symbol across different synthetic mints must be blocked
        ok1, _, _ = executor.execute_entry(book, "BORKMint1", _clean_facts(symbol="BORK", price_usd=1.0), 82, mode="demo")
        ok2, msg2, _ = executor.execute_entry(book, "BORKMint2", _clean_facts(symbol="BORK", price_usd=1.0), 95, mode="demo")
        self.assertTrue(ok1)
        self.assertFalse(ok2)
        self.assertIn("BORK", msg2)

        # 2) Dynamic trailing stop above +120% must raise stop floor above +80% and exit on pullback
        book["open"]["BORKMint1"]["last_price"] = 4.0  # +300%
        risk.monitor_positions(book, mode="live")
        self.assertIn("BORKMint1", book["open"])
        self.assertGreaterEqual(book["open"]["BORKMint1"]["stop_pct"], 220.0)
        book["open"]["BORKMint1"]["last_price"] = 3.1  # +210%, below 225% dynamic trail
        evs_trail = risk.monitor_positions(book, mode="live")
        self.assertEqual(len(book["open"]), 0)
        self.assertTrue(any("trail stop" in e["msg"] for e in evs_trail))

        # 3) Long demo soak (2,500 passes) must remain bounded (never compounding to trillions)
        book = executor.load_book()
        rng = random.Random(42)
        for i in range(2500):
            sym = f"T{i % 18}"
            mint = f"{sym}_{i}"
            if len(book["open"]) < config.MAX_OPEN_POSITIONS and i % 4 == 0:
                executor.execute_entry(book, mint, _clean_facts(symbol=sym, price_usd=0.01), 80, mode="demo")
            risk.monitor_positions(book, mode="demo", rng=rng)
            if config.STOP_FILE.exists():
                config.STOP_FILE.unlink()
        eq = executor.equity_of(book)
        self.assertLess(eq, 5_000.0)


if __name__ == "__main__":
    unittest.main()
