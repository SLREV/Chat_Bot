"""Unit tests for the 6-agent trading desk (Python stdlib unittest)."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

DESK_DIR = Path(__file__).resolve().parent.parent
if str(DESK_DIR) not in sys.path:
    sys.path.insert(0, str(DESK_DIR))

import config  # noqa: E402
import crew  # noqa: E402
from agents import analyst, executor, filter_agent, risk  # noqa: E402


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

    def test_crew_demo_pass_writes_state_and_logs(self) -> None:
        st = crew.run_pass(mode="demo", use_grok=False)
        self.assertTrue(config.STATE_FILE.exists())
        self.assertTrue(config.LOG_FILE.exists())
        self.assertGreater(st["scanned"], 0)
        saved = json.loads(config.STATE_FILE.read_text(encoding="utf-8"))
        self.assertEqual(set(saved["agents"].keys()), {"scout", "filter", "analyst", "executor", "risk", "logger"})


if __name__ == "__main__":
    unittest.main()
