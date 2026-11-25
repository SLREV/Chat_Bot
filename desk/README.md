# 6-Agent Trading Desk (`desk/`)

Implementasi ringan (Python 3 stdlib saja, **0 dependensi pip**) dari desk 6 agen untuk Solana memes (**paper mode default**):

| Agen | File | Peran |
|---|---|---|
| **Scout** | `agents/scout.py` | Memindai pair baru (`Pump.fun + Raydium`) dari DexScreener (`--mode live`) atau demo feed (`--mode demo`) |
| **Filter** | `agents/filter_agent.py` | 8 hard kill rules deterministik (*fail-closed*: data kosong/error otomatis ditolak) |
| **Analyst** | `agents/analyst.py` | Menilai survivor Filter (`score 70+ only`), mendukung optional Grok `x_search` bila `XAI_API_KEY` diset |
| **Executor** | `agents/executor.py` | Sizing maks **8% equity** per posisi, maks 3 posisi terbuka, paper fill + Jupiter quote |
| **Risk** | `agents/risk.py` | Hard stop **-6%**, trailing stop mulai **+12%**, take-half **+100%**, circuit breaker harian **-15%** (`data/STOP`) |
| **Logger** | `agents/logger_agent.py` | Menulis `data/desk_log.jsonl` dan `data/state.json` secara atomik |

## Menjalankan

```bash
# 1. Jalankan 1 pass uji offline (demo)
python3 desk/crew.py --once --mode demo

# 2. Jalankan dashboard ringan + background loop 6 agen (default 127.0.0.1:8788 + token auth)
python3 desk/dashboard.py --run-crew --mode demo --port 8788

# 3. Mode live (DexScreener + Rugcheck + Jupiter quote, tetap paper fill)
python3 desk/dashboard.py --run-crew --mode live --port 8788

# Opsional: set token statis atau bind ke interface tertentu (gunakan hanya di jaringan tepercaya / di balik reverse proxy TLS)
DESK_AUTH_TOKEN=rahasia-anda python3 desk/dashboard.py --run-crew --bind 127.0.0.1 --port 8788

# 4. Kontrol darurat
python3 desk/crew.py --stop
python3 desk/crew.py --resume
```

## Pengujian

```bash
python3 -m unittest discover -s desk/tests -v
```
