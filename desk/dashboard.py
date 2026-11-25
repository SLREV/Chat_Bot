#!/usr/bin/env python3
"""Lightweight zero-dependency HTTP + SSE dashboard for the 6-agent desk."""

from __future__ import annotations

import argparse
import hmac
import json
import os
import secrets
import sys
import threading
import time
from collections import deque
from http import HTTPStatus
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, quote, urlparse

DESK_DIR = Path(__file__).resolve().parent
if str(DESK_DIR) not in sys.path:
    sys.path.insert(0, str(DESK_DIR))

import config  # noqa: E402
import crew  # noqa: E402
from agents import executor, logger_agent  # noqa: E402

_MODE_LOCK = threading.Lock()
_RUNTIME = {"mode": "demo", "grok": False, "interval": 6.0, "crew_running": False}
_SSE_SUBS: dict[int, deque[bytes]] = {}
_SSE_LOCK = threading.Lock()


def _broadcast_state(state: dict[str, Any]) -> None:
    payload = f"data: {json.dumps(state)}\n\n".encode("utf-8")
    with _SSE_LOCK:
        dead = []
        for qid, q in _SSE_SUBS.items():
            try:
                q.append(payload)
            except Exception:
                dead.append(qid)
        for qid in dead:
            _SSE_SUBS.pop(qid, None)


HTML = r"""<!doctype html>
<html lang="id">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Grok Bot · 6-Agent Desk</title>
<style>
  :root{
    --bg:#f4f7f9; --card:#ffffff; --card2:#f8fafc; --line:#e3e8ee;
    --text:#101828; --muted:#667085; --green:#16a34a; --green-bg:#dcfce7;
    --red:#dc2626; --red-bg:#fee2e2; --amber:#d97706; --amber-bg:#fef3c7;
    --blue:#0284c7; --blue-bg:#e0f2fe; --purple:#7c3aed; --purple-bg:#ede9fe;
    --cyan:#0891b2; --cyan-bg:#cffafe; --radius:14px;
    --mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
    --sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif;
  }
  @media (prefers-color-scheme: dark){
    :root{
      --bg:#0d1117; --card:#151b23; --card2:#1b222c; --line:#26303d;
      --text:#e6edf3; --muted:#8b949e; --green:#3fb950; --green-bg:rgba(63,185,80,.15);
      --red:#f85149; --red-bg:rgba(248,81,73,.15); --amber:#d29922; --amber-bg:rgba(210,153,34,.15);
      --blue:#58a6ff; --blue-bg:rgba(88,166,255,.15); --purple:#a371f7; --purple-bg:rgba(163,113,247,.15);
      --cyan:#39c5cf; --cyan-bg:rgba(57,197,207,.15);
    }
  }
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--text);font-family:var(--sans);font-size:14px;-webkit-font-smoothing:antialiased}
  .wrap{max-width:860px;margin:0 auto;padding:14px 14px 36px;display:grid;gap:12px}
  .head{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}
  .brand{display:flex;align-items:center;gap:10px}
  .logo{width:38px;height:38px;border-radius:11px;display:grid;place-items:center;background:#111827;color:#fff;font-size:20px;font-weight:800;box-shadow:0 2px 8px rgba(0,0,0,.15)}
  .brand h1{margin:0;font-size:18px;letter-spacing:-.2px}
  .brand small{color:var(--muted);font-size:12px;display:block}
  .status-row{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
  .dot-live{display:inline-flex;align-items:center;gap:6px;font-size:12px;font-weight:700;padding:4px 10px;border-radius:999px;background:var(--green-bg);color:var(--green)}
  .dot-live.paused{background:var(--red-bg);color:var(--red)}
  .dot-live i{width:7px;height:7px;border-radius:50%;background:currentColor}
  .btn{border:1px solid var(--line);background:var(--card);color:var(--text);padding:6px 11px;border-radius:9px;font-size:12px;font-weight:600;cursor:pointer}
  .btn:hover{border-color:var(--blue)}
  .btn.pri{background:var(--blue);border-color:transparent;color:#fff}
  .btn.danger{color:var(--red)}
  .ticker{display:flex;gap:12px;overflow-x:auto;padding:8px 12px;background:var(--card);border:1px solid var(--line);border-radius:12px;font-size:12px;white-space:nowrap}
  .tick-item{display:inline-flex;align-items:center;gap:6px;font-family:var(--mono)}
  .pos{color:var(--green);font-weight:700}
  .neg{color:var(--red);font-weight:700}
  .card{background:var(--card);border:1px solid var(--line);border-radius:var(--radius);padding:14px 16px}
  .eq-top{display:flex;justify-content:space-between;align-items:flex-start;gap:10px}
  .eq-val{font-size:34px;font-weight:800;letter-spacing:-1px;margin:4px 0}
  .mult{background:var(--green-bg);color:var(--green);font-family:var(--mono);font-weight:700;font-size:12px;padding:4px 9px;border-radius:8px}
  .card-title{display:flex;justify-content:space-between;align-items:center;margin-bottom:10px}
  .card-title h2{margin:0;font-size:15px}
  .muted{color:var(--muted);font-size:12px}
  .agents{display:grid;gap:8px}
  .ag{display:grid;grid-template-columns:34px 120px 1fr auto;align-items:center;gap:10px;padding:7px 6px;border-radius:10px}
  .ag:hover{background:var(--card2)}
  .ag-ico{width:32px;height:32px;border-radius:9px;display:grid;place-items:center;font-size:16px}
  .ag-name b{display:block;font-size:13.5px}
  .ag-name span{display:block;font-size:11px;color:var(--muted)}
  .ag-last{font-size:12.5px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-family:var(--mono)}
  .pill{font-family:var(--mono);font-size:11px;font-weight:700;padding:3px 9px;border-radius:999px;white-space:nowrap}
  .p-blue{background:var(--blue-bg);color:var(--blue)}
  .p-red{background:var(--red-bg);color:var(--red)}
  .p-amber{background:var(--amber-bg);color:var(--amber)}
  .p-green{background:var(--green-bg);color:var(--green)}
  .p-purple{background:var(--purple-bg);color:var(--purple)}
  .p-cyan{background:var(--cyan-bg);color:var(--cyan)}
  .logs{display:grid;gap:6px;max-height:240px;overflow:auto;font-family:var(--mono);font-size:12px}
  .log-row{display:flex;gap:8px;align-items:baseline;padding:3px 0;border-bottom:1px dashed var(--line)}
  .log-row:last-child{border-bottom:none}
  .log-ts{color:var(--muted);flex:none}
  .log-ag{font-weight:700;flex:none}
  .rules{display:grid;grid-template-columns:1fr 1fr;gap:8px 18px;font-size:12.5px}
  .rule{display:flex;justify-content:space-between;align-items:center;padding:5px 0;border-bottom:1px solid var(--line)}
  .rule span{color:var(--muted)}
  .rule b{color:var(--red);font-family:var(--mono)}
  .pos-list{display:grid;gap:8px}
  .pos-row{display:flex;justify-content:space-between;align-items:center;gap:10px;padding:8px 10px;border:1px solid var(--line);border-radius:10px;background:var(--card2);font-family:var(--mono);font-size:12px}
  @media (max-width:640px){
    .ag{grid-template-columns:32px 96px 1fr auto}
    .rules{grid-template-columns:1fr}
  }
</style>
</head>
<body>
<div class="wrap">
  <div class="head">
    <div class="brand">
      <div class="logo">🤖</div>
      <div>
        <h1>Grok Bot <span class="muted" id="modeTag">· paper</span></h1>
        <small>Solana memes · strict RM · 6-agent Chief of Staff desk</small>
      </div>
    </div>
    <div class="status-row">
      <span class="dot-live" id="liveBadge"><i></i><span id="liveTxt">Live</span></span>
      <button class="btn pri" id="passBtn">▶ 1 Pass</button>
      <button class="btn" id="modeBtn">Mode: Demo</button>
      <button class="btn danger" id="stopBtn">⏸ Stop</button>
      <button class="btn" id="resetBtn">↻ Reset</button>
    </div>
  </div>

  <div class="ticker" id="ticker"><span class="muted">Menunggu sinyal Scout…</span></div>

  <div class="card">
    <div class="eq-top">
      <div>
        <div class="muted" id="eqSub">Equity · $150 start · 8% max size</div>
        <div class="eq-val" id="eqVal">$150.00</div>
        <div id="eqPnl" class="pos">+$0.00 (0.00%)</div>
      </div>
      <div style="text-align:right">
        <span class="mult" id="multVal">1.00×</span>
        <div class="muted" style="margin-top:10px" id="eqMeta">0 scanned · 0 traded</div>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-title">
      <h2>Desk · 6 agents</h2>
      <span class="muted" id="deskCounts">0 scanned · 0 traded</span>
    </div>
    <div class="agents" id="agents"></div>
  </div>

  <div class="card" id="posCard" style="display:none">
    <div class="card-title">
      <h2>Open positions</h2>
      <span class="muted" id="posCount">0 open</span>
    </div>
    <div class="pos-list" id="posList"></div>
  </div>

  <div class="card">
    <div class="card-title">
      <h2>Logger</h2>
      <span class="muted" id="killedTotal">0 killed</span>
    </div>
    <div class="logs" id="logs"><div class="muted">Belum ada event.</div></div>
  </div>

  <div class="card">
    <div class="card-title">
      <h2>Killed by rule</h2>
      <span class="muted">8 hard rules · fail-closed</span>
    </div>
    <div class="rules" id="rules"></div>
  </div>
</div>

<script>
(function(){
  "use strict";
  const $ = (s)=>document.querySelector(s);
  const esc = (s)=>String(s??"").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));

  const AGENT_META = [
    {key:"scout",    name:"Scout",    sub:"new pairs",      ico:"🔍", cls:"p-blue",   bg:"var(--blue-bg)"},
    {key:"filter",   name:"Filter",   sub:"8 kill rules",   ico:"▽",  cls:"p-red",    bg:"var(--amber-bg)"},
    {key:"analyst",  name:"Analyst",  sub:"score 70+ only", ico:"🛡", cls:"p-blue",   bg:"var(--blue-bg)"},
    {key:"executor", name:"Executor", sub:"8% max size",    ico:"⚡", cls:"p-green",  bg:"var(--green-bg)"},
    {key:"risk",     name:"Risk",     sub:"-6% stop · trail",ico:"◔", cls:"p-purple", bg:"var(--purple-bg)"},
    {key:"logger",   name:"Logger",   sub:"reason for all", ico:"📄", cls:"p-cyan",   bg:"var(--cyan-bg)"},
  ];

  function render(st){
    if(!st) return;
    const stopped = !!st.stopped;
    $("#liveBadge").className = "dot-live" + (stopped ? " paused" : "");
    $("#liveTxt").textContent = stopped ? "Paused (STOP)" : "Live";
    $("#stopBtn").textContent = stopped ? "▶ Resume" : "⏸ Stop";
    const execLabel = st.live_armed ? `LIVE ON-CHAIN (cap $${Number(st.max_live_trade_usd||5).toFixed(0)})` : (st.paper ? "paper" : "live-locked");
    $("#modeTag").textContent = "· " + execLabel + " · " + (st.mode || "demo");
    $("#modeBtn").textContent = "Mode: " + ((st.mode||"demo") === "live" ? "Live" : "Demo");

    const eq = Number(st.equity_usd || 0);
    const pnl = Number(st.pnl_usd || 0);
    const pnlPct = Number(st.pnl_pct || 0);
    $("#eqVal").textContent = "$" + eq.toFixed(2);
    $("#eqPnl").textContent = `${pnl>=0?"+":""}$${pnl.toFixed(2)} (${pnlPct>=0?"+":""}${pnlPct.toFixed(2)}%)`;
    $("#eqPnl").className = pnl >= 0 ? "pos" : "neg";
    $("#multVal").textContent = Number(st.multiplier || 1).toFixed(2) + "×";
    $("#eqMeta").textContent = `${st.scanned||0} scanned · ${st.traded||0} traded`;
    $("#deskCounts").textContent = `${st.scanned||0} scanned · ${st.traded||0} traded`;
    $("#killedTotal").textContent = `${st.killed_total||0} killed`;

    const tick = st.ticker || [];
    $("#ticker").innerHTML = tick.length ? tick.map(t=>
      `<span class="tick-item"><b>$${esc(t.symbol)}</b> <span>$${Number(t.price).toFixed(5)}</span> <span class="${t.chg>=0?"pos":"neg"}">${t.chg>=0?"+":""}${Number(t.chg).toFixed(1)}%</span></span>`
    ).join("") : `<span class="muted">Menunggu sinyal Scout…</span>`;

    const agMap = st.agents || {};
    $("#agents").innerHTML = AGENT_META.map(m=>{
      const d = agMap[m.key] || {};
      return `<div class="ag">
        <div class="ag-ico" style="background:${m.bg}">${m.ico}</div>
        <div class="ag-name"><b>${m.name}</b><span>${m.sub}</span></div>
        <div class="ag-last" title="${esc(d.last||"")}">${esc(d.last||"-")}</div>
        <span class="pill ${m.cls}">${esc(d.badge||"")}</span>
      </div>`;
    }).join("");

    const open = st.open_positions || [];
    $("#posCard").style.display = open.length ? "block" : "none";
    $("#posCount").textContent = `${open.length} open`;
    const isB58 = (s)=>typeof s==="string" && /^[1-9A-HJ-NP-Za-km-z]{32,100}$/.test(s);
    $("#posList").innerHTML = open.map(p=>{
      const txHtml = isB58(p.tx_sig) ? ` · <a href="https://solscan.io/tx/${esc(p.tx_sig)}" target="_blank" rel="noopener noreferrer" style="color:var(--blue)">tx</a>` : "";
      const exTag = p.execution==="live" ? "LIVE" : "paper";
      return `<div class="pos-row">
        <div><b>$${esc(p.symbol)}</b> <span class="muted">[${exTag}] score ${p.score} · stop ${p.stop_pct}%${txHtml}</span></div>
        <div>$${Number(p.current_usd).toFixed(2)} <span class="${p.gain_pct>=0?"pos":"neg"}">${p.gain_pct>=0?"+":""}${Number(p.gain_pct).toFixed(1)}%</span></div>
      </div>`;
    }).join("");

    const logs = (st.logs || []).slice(0, 30);
    $("#logs").innerHTML = logs.length ? logs.map(l=>{
      const cls = l.level==="ok" ? "pos" : (l.level==="err" ? "neg" : "");
      return `<div class="log-row"><span class="log-ts">${esc(l.hhmm||"")}</span><span class="log-ag ${cls}">${esc(l.agent)}:</span><span>${esc(l.msg)}</span></div>`;
    }).join("") : `<div class="muted">Belum ada event.</div>`;

    const kr = st.killed_by_rule || {};
    $("#rules").innerHTML = Object.keys(kr).map(k=>
      `<div class="rule"><span>${esc(k)}</span><b>${kr[k]||0}</b></div>`
    ).join("");
  }

  async function post(path, body){
    const r = await fetch(path, {
      method:"POST",
      headers:{"content-type":"application/json","x-desk-csrf":"1"},
      body:JSON.stringify(body||{})
    });
    if(r.status===401){ location.reload(); return; }
    if(!r.ok) return;
    const st = await r.json();
    render(st);
  }

  $("#passBtn").onclick = ()=>post("/api/pass");
  $("#stopBtn").onclick = ()=>post("/api/control", {action: $("#stopBtn").textContent.includes("Resume") ? "resume" : "stop"});
  $("#modeBtn").onclick = ()=>{
    const next = $("#modeBtn").textContent.includes("Demo") ? "live" : "demo";
    post("/api/control", {action:"mode", mode:next});
  };
  $("#resetBtn").onclick = ()=>post("/api/reset");

  fetch("/api/state").then(r=>{
    if(r.status===401){ location.reload(); return null; }
    return r.json();
  }).then(st=>{ if(st) render(st); }).catch(()=>{});
  const es = new EventSource("/api/events");
  es.onmessage = (ev)=>{ try{ render(JSON.parse(ev.data)); }catch{} };
})();
</script>
</body>
</html>
"""

LOGIN_HTML = r"""<!doctype html>
<html lang="id">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Masuk · 6-Agent Desk</title>
<style>
  *{box-sizing:border-box}
  body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0d1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif;padding:16px}
  .box{width:100%;max-width:380px;background:#151b23;border:1px solid #26303d;border-radius:14px;padding:20px;display:grid;gap:12px}
  h1{margin:0;font-size:17px}
  p{margin:0;color:#8b949e;font-size:13px;line-height:1.45}
  input{width:100%;padding:10px 12px;border-radius:9px;border:1px solid #26303d;background:#0d1117;color:#e6edf3;font-size:14px}
  button{padding:10px 12px;border-radius:9px;border:0;background:#58a6ff;color:#0d1117;font-weight:700;font-size:14px;cursor:pointer}
  .err{color:#f85149;font-size:12px;min-height:16px}
</style>
</head>
<body>
<form class="box" id="f">
  <h1>🔐 6-Agent Trading Desk</h1>
  <p>Masukkan token akses dari terminal saat dashboard dijalankan, atau buka URL dengan <code>?token=...</code>.</p>
  <input id="tok" type="password" placeholder="Token akses" autocomplete="current-password" required>
  <button type="submit">Masuk</button>
  <div class="err" id="err"></div>
</form>
<script>
document.getElementById("f").onsubmit = async (e) => {
  e.preventDefault();
  const err = document.getElementById("err");
  err.textContent = "";
  const token = document.getElementById("tok").value.trim();
  const r = await fetch("/api/auth", {
    method: "POST",
    headers: {"content-type": "application/json", "x-desk-csrf": "1"},
    body: JSON.stringify({token})
  });
  if (r.ok) { location.replace("/"); return; }
  err.textContent = "Token tidak valid.";
};
</script>
</body>
</html>
"""


def ensure_auth_token() -> str:
    env_tok = os.environ.get("DESK_AUTH_TOKEN", "").strip()
    if env_tok:
        return env_tok
    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    if config.TOKEN_FILE.exists():
        saved = config.TOKEN_FILE.read_text(encoding="utf-8").strip()
        if saved:
            try:
                os.chmod(config.TOKEN_FILE, 0o600)
            except OSError:
                pass
            return saved
    tok = secrets.token_urlsafe(32)
    fd = os.open(str(config.TOKEN_FILE), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.write(fd, (tok + "\n").encode("utf-8"))
    finally:
        os.close(fd)
    return tok


class DeskHTTPServer(ThreadingHTTPServer):
    def __init__(self, server_address: tuple[str, int], RequestHandlerClass: type[BaseHTTPRequestHandler], auth_token: str) -> None:
        self.auth_token = auth_token
        super().__init__(server_address, RequestHandlerClass)


def _token_equal(a: str, b: str) -> bool:
    return bool(a and b and hmac.compare_digest(a.encode("utf-8"), b.encode("utf-8")))


class DeskHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, format: str, *args: Any) -> None:
        return  # Keep terminal output quiet and RAM tiny

    def _expected_token(self) -> str:
        return str(getattr(self.server, "auth_token", ""))

    def _extract_token(self) -> tuple[str, bool]:
        """Return (token, came_from_cookie)."""
        auth_hdr = (self.headers.get("Authorization") or "").strip()
        if auth_hdr.lower().startswith("bearer "):
            return auth_hdr[7:].strip(), False
        hdr_tok = (self.headers.get("X-Desk-Token") or "").strip()
        if hdr_tok:
            return hdr_tok, False
        raw_cookie = self.headers.get("Cookie") or ""
        if raw_cookie:
            jar = SimpleCookie()
            try:
                jar.load(raw_cookie)
            except Exception:
                return "", False
            morsel = jar.get("desk_token")
            if morsel and morsel.value:
                return morsel.value.strip(), True
        return "", False

    def _is_authed(self) -> tuple[bool, bool]:
        tok, from_cookie = self._extract_token()
        return _token_equal(tok, self._expected_token()), from_cookie

    def _send_security_headers(self) -> None:
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")

    def _set_session_cookie(self, token: str) -> None:
        proto = (self.headers.get("X-Forwarded-Proto") or "").lower()
        secure = "; Secure" if proto == "https" else ""
        self.send_header("Set-Cookie", f"desk_token={token}; Path=/; HttpOnly; SameSite=Strict{secure}")

    def _check_csrf(self, is_write: bool, auth_via_cookie: bool) -> bool:
        sfs = (self.headers.get("Sec-Fetch-Site") or "").strip().lower()
        if sfs == "cross-site":
            self._send_json(403, {"error": "cross-site request rejected"})
            return False

        host = (self.headers.get("Host") or "").strip()
        origin = (self.headers.get("Origin") or "").strip()
        if origin:
            u = urlparse(origin)
            if not u.netloc or not host or u.netloc.lower() != host.lower():
                self._send_json(403, {"error": "origin not allowed"})
                return False
        elif is_write:
            ref = (self.headers.get("Referer") or "").strip()
            if ref:
                u = urlparse(ref)
                if not u.netloc or not host or u.netloc.lower() != host.lower():
                    self._send_json(403, {"error": "referer not allowed"})
                    return False

        if is_write:
            ct = (self.headers.get("Content-Type") or "").split(";", 1)[0].strip().lower()
            try:
                clen = int(self.headers.get("Content-Length") or "0")
            except ValueError:
                clen = 0
            if (ct != "" or clen != 0) and ct != "application/json":
                self._send_json(415, {"error": "Content-Type must be application/json"})
                return False
            if auth_via_cookie and (self.headers.get("X-Desk-CSRF") or "").strip() != "1":
                self._send_json(403, {"error": "missing X-Desk-CSRF header"})
                return False
        return True

    def _send_json(self, code: int, obj: Any, extra_headers: dict[str, str] | None = None) -> None:
        raw = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self._send_security_headers()
        if extra_headers:
            for k, v in extra_headers.items():
                self.send_header(k, v)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _send_html(self, code: int, body: str) -> None:
        raw = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self._send_security_headers()
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _read_json(self) -> dict[str, Any]:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length <= 0 or length > 65536:
            return {}
        raw = self.rfile.read(length)
        try:
            data = json.loads(raw.decode("utf-8"))
            return data if isinstance(data, dict) else {}
        except Exception:
            return {}

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/api/health":
            self._send_json(200, {"ok": True, "service": "grok-6-agent-desk"})
            return

        if path == "/" or path == "/index.html":
            qs = parse_qs(parsed.query)
            qtok = (qs.get("token") or [""])[0].strip()
            if qtok and _token_equal(qtok, self._expected_token()):
                self.send_response(HTTPStatus.FOUND)
                self._send_security_headers()
                self._set_session_cookie(self._expected_token())
                self.send_header("Location", "/")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return

            authed, _ = self._is_authed()
            if not authed:
                self._send_html(HTTPStatus.UNAUTHORIZED, LOGIN_HTML)
                return
            self._send_html(HTTPStatus.OK, HTML)
            return

        authed, _ = self._is_authed()
        if not authed:
            self._send_json(
                401,
                {"error": "authentication required"},
                {"WWW-Authenticate": 'Bearer realm="Desk"'},
            )
            return

        if path == "/api/state":
            self._send_json(200, logger_agent.load_state())
            return

        if path == "/api/events":
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-Type", "text/event-stream")
            self._send_security_headers()
            self.send_header("Connection", "keep-alive")
            self.end_headers()

            queue: deque[bytes] = deque(maxlen=32)
            qid = id(queue)
            with _SSE_LOCK:
                _SSE_SUBS[qid] = queue
            try:
                init = f"data: {json.dumps(logger_agent.load_state())}\n\n".encode("utf-8")
                self.wfile.write(init)
                self.wfile.flush()
                last_ping = time.time()
                while True:
                    if queue:
                        chunk = queue.popleft()
                        self.wfile.write(chunk)
                        self.wfile.flush()
                        last_ping = time.time()
                    elif time.time() - last_ping >= 15.0:
                        self.wfile.write(b": ping\n\n")
                        self.wfile.flush()
                        last_ping = time.time()
                    time.sleep(0.25)
            except Exception:
                pass
            finally:
                with _SSE_LOCK:
                    _SSE_SUBS.pop(qid, None)
            return

        self._send_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        path = urlparse(self.path).path

        if path == "/api/auth":
            if not self._check_csrf(is_write=True, auth_via_cookie=True):
                return
            body = self._read_json()
            tok = str(body.get("token") or "").strip()
            if not _token_equal(tok, self._expected_token()):
                self._send_json(401, {"error": "invalid token"})
                return
            raw = json.dumps({"ok": True}).encode("utf-8")
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self._send_security_headers()
            self._set_session_cookie(self._expected_token())
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return

        authed, from_cookie = self._is_authed()
        if not authed:
            self._send_json(
                401,
                {"error": "authentication required"},
                {"WWW-Authenticate": 'Bearer realm="Desk"'},
            )
            return

        if not self._check_csrf(is_write=True, auth_via_cookie=from_cookie):
            return

        body = self._read_json()

        if path == "/api/pass":
            with _MODE_LOCK:
                mode = str(_RUNTIME["mode"])
                grok = bool(_RUNTIME["grok"])
            st = crew.run_pass(mode=mode, use_grok=grok)
            _broadcast_state(st)
            self._send_json(200, st)
            return

        if path == "/api/control":
            act = str(body.get("action") or "")
            config.DATA_DIR.mkdir(parents=True, exist_ok=True)
            if act == "stop":
                config.STOP_FILE.write_text("Paused from dashboard\n", encoding="utf-8")
            elif act == "resume":
                if config.STOP_FILE.exists():
                    config.STOP_FILE.unlink()
            elif act == "mode":
                m = str(body.get("mode") or "demo")
                if m in ("demo", "live"):
                    with _MODE_LOCK:
                        _RUNTIME["mode"] = m
            st = logger_agent.load_state()
            with _MODE_LOCK:
                st["mode"] = _RUNTIME["mode"]
            st["stopped"] = config.STOP_FILE.exists()
            st["agents"]["executor"]["status"] = "warn" if st["stopped"] else "ok"
            st["agents"]["executor"]["last"] = "PAUSED (STOP active)" if st["stopped"] else st["agents"]["executor"]["last"]
            logger_agent.save_state(st)
            _broadcast_state(st)
            self._send_json(200, st)
            return

        if path == "/api/reset":
            # Jangan izinkan reset dari dashboard bila masih ada posisi live on-chain terbuka
            book = executor.load_book()
            live_open = [p for p in book.get("open", {}).values() if p.get("execution") == "live" and int(p.get("token_amount_raw") or 0) > 0]
            if live_open:
                self._send_json(409, {"error": "cannot reset while live on-chain positions remain open"})
                return
            with _MODE_LOCK:
                mode = str(_RUNTIME["mode"])
            st = crew.reset_desk(mode=mode)
            _broadcast_state(st)
            self._send_json(200, st)
            return

        self._send_json(404, {"error": "not found"})


def _crew_worker() -> None:
    while True:
        with _MODE_LOCK:
            mode = str(_RUNTIME["mode"])
            grok = bool(_RUNTIME["grok"])
            interval = float(_RUNTIME["interval"])
        try:
            st = crew.run_pass(mode=mode, use_grok=grok)
            _broadcast_state(st)
        except Exception:
            pass
        time.sleep(max(2.0, interval))


def main() -> int:
    parser = argparse.ArgumentParser(description="Lightweight 6-Agent Desk Dashboard")
    parser.add_argument("--bind", default=config.DASHBOARD_BIND, help="bind address (default 127.0.0.1)")
    parser.add_argument("--port", type=int, default=config.DASHBOARD_PORT, help="HTTP port (default 8788)")
    parser.add_argument("--mode", choices=("demo", "live"), default="demo", help="initial feed mode")
    parser.add_argument("--interval", type=float, default=6.0, help="crew loop interval in seconds")
    parser.add_argument("--run-crew", action="store_true", help="also run the 6-agent crew loop in background")
    parser.add_argument("--grok", action="store_true", help="enable Grok x_search in Analyst when XAI_API_KEY is set")
    args = parser.parse_args()

    with _MODE_LOCK:
        _RUNTIME["mode"] = args.mode
        _RUNTIME["interval"] = args.interval
        _RUNTIME["grok"] = args.grok
        _RUNTIME["crew_running"] = args.run_crew

    config.DATA_DIR.mkdir(parents=True, exist_ok=True)
    auth_token = ensure_auth_token()
    if not config.STATE_FILE.exists():
        st = crew.run_pass(mode=args.mode, use_grok=args.grok)
    else:
        st = logger_agent.load_state()
        st["mode"] = args.mode
        logger_agent.save_state(st)

    if args.run_crew:
        t = threading.Thread(target=_crew_worker, daemon=True)
        t.start()

    srv = DeskHTTPServer((args.bind, args.port), DeskHandler, auth_token=auth_token)
    display_host = "127.0.0.1" if args.bind in ("0.0.0.0", "") else args.bind
    print(
        f"6-Agent Desk Dashboard listening on http://{args.bind}:{args.port} (mode={args.mode}, crew={args.run_crew})\n"
        f"Auth URL: http://{display_host}:{args.port}/?token={quote(auth_token)}",
        flush=True,
    )
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down dashboard.")
    finally:
        srv.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
