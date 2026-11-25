# 6-Agent Trading Desk (`desk/`)

Implementasi portabel desk 6 agen untuk Solana memes. **Tahap 1 & Tahap 2 memakai Python 3 stdlib saja (0 dependensi pip)**; `solders` hanya dibutuhkan untuk **Tahap 3 (signing transaksi live)**.

| Agen | File | Peran |
|---|---|---|
| **Scout** | `agents/scout.py` | Memindai pair baru (`Pump.fun + Raydium`) dari DexScreener (`--mode live`) atau demo feed (`--mode demo`) |
| **Filter** | `agents/filter_agent.py` | 8 hard kill rules deterministik (*fail-closed*: data kosong/error otomatis ditolak) |
| **Analyst** | `agents/analyst.py` | Menilai survivor Filter (`score 70+ only`), mendukung optional Grok `x_search` bila `XAI_API_KEY` diset |
| **Executor** | `agents/executor.py` | Sizing maks **8% equity**, maks 3 posisi; Tahap 1 paper fill + Jupiter quote; Tahap 3 live BUY (cap burner wallet) |
| **Risk** | `agents/risk.py` | Hard stop **-6%**, trailing stop mulai **+12%**, take-half **+100%**, max hold **4h**, circuit breaker **-15%** (`data/STOP`); di mode live menjual 50% / 100% token via Jupiter |
| **Wallet** | `agents/wallet.py` | Solana JSON-RPC, Jupiter Quote/Swap builder, `simulateTransaction` dry-run, dan signer Ed25519 (`solders`) |
| **Logger** | `agents/logger_agent.py` | Menulis `data/desk_log.jsonl` dan `data/state.json` secara atomik |

---

## Alur Migrasi 3 Tahap (Portabel & Aman)

### Tahap 1 — Uji `--mode live` dengan `PAPER = True` (0 dependensi pip)
Data pasar, Filter, Analyst, dan Jupiter quote memakai data nyata, tetapi order tetap paper:
```bash
python3 desk/crew.py --doctor
python3 desk/crew.py --once --mode live
python3 desk/dashboard.py --run-crew --mode live --port 8788
```

### Tahap 2 — Dry-Run `simulateTransaction` via Solana RPC (tanpa private key)
Salin `.env.example` menjadi `.env` (otomatis dibaca `desk/config.py` dan di-ignore Git), isi `SOLANA_RPC_URL` & `SOLANA_PUBLIC_KEY`:
```bash
cp .env.example .env && chmod 600 .env
# isi SOLANA_RPC_URL=... dan SOLANA_PUBLIC_KEY=... di .env
python3 desk/crew.py --simulate <MINT_SOLANA> --usd 2.0
```

### Tahap 3 — Live On-Chain dengan Burner Wallet & Double-Lock
Pasang signer ringan (`solders`), lalu aktifkan **keempat** variabel di `.env` Anda:
```bash
pip install -r desk/requirements-live.txt

# Di file .env:
#   DESK_EXECUTION_MODE=live
#   DESK_LIVE_CONFIRM=I_UNDERSTAND_REAL_MONEY
#   SOLANA_RPC_URL=https://<rpc-anda>
#   SOLANA_PRIVATE_KEY=<base58-atau-path-ke-keypair-burner.json>
#   DESK_MAX_LIVE_TRADE_USD=5.0

python3 desk/crew.py --doctor
python3 desk/dashboard.py --run-crew --mode live --port 8788
```

---

## Kontrol Darurat & Pengujian

```bash
# Kontrol darurat
python3 desk/crew.py --stop
python3 desk/crew.py --resume

# Unit test (termasuk simulasi RPC, double-lock, take-half, & exit live)
python3 -m unittest discover -s desk/tests -v
```
