# Panduan Instalasi & Menjalankan OpenMausBot + 6-Agent Desk

Repo ini memiliki dua komponen yang bisa dijalankan terpisah maupun bersamaan:

1. **OpenMausBot (`omb`, Go)** — aplikasi chat bot multi-agent ringan (~7 MB RAM, UI web di `http://127.0.0.1:8799`).
2. **6-Agent Trading Desk (`desk/`, Python 3)** — desk `Scout → Filter → Analyst → Executor → Risk → Logger` + dashboard live (`http://127.0.0.1:8788`).

---

## 1. Prasyarat

| Komponen | Kebutuhan Minimum | Wajib? |
|---|---|---|
| **Git** | versi apa saja | Ya (untuk clone) |
| **Go** | **1.24+** | Wajib hanya untuk OpenMausBot (`omb`) |
| **Python** | **3.10+** (hanya stdlib, 0 pip) | Wajib untuk `desk/` |
| **Live Signing Solana** *(opsional)* | `solders>=0.21.0` (`desk/requirements-live.txt`) | Hanya untuk Tahap 3 live on-chain |

Clone repository:
```sh
git clone https://github.com/SLREV/Chat_Bot.git
cd Chat_Bot
```

---

## 2. Menjalankan OpenMausBot (`omb`)

### A. Tanpa Build (langsung jalan dari source Go)
Cocok saat development atau mencoba cepat tanpa menghasilkan binary:
```sh
# Cara 1 (langsung via Go)
go run ./cmd/omb

# Cara 2 (lewat Makefile)
make run
```

Opsi berguna saat dijalankan tanpa build:
```sh
go run ./cmd/omb --port 9000         # ganti port (default 8799)
go run ./cmd/omb --open=false        # mode headless / server tanpa buka browser
go run ./cmd/omb --bind 0.0.0.0      # izinkan akses dari perangkat lain di LAN
go run ./cmd/omb doctor              # cek kesiapan port, CLI, dan API key
go run ./cmd/omb check               # uji koneksi nyata ke provider API
```

> **Catatan Token Login:** Saat server menyala, terminal mencetak URL beserta token otomatis, misalnya `http://127.0.0.1:8799/?token=...`. Buka URL tersebut agar cookie sesi langsung aktif. Anda juga bisa menentukan token tetap dengan `--token rahasia` atau `OMB_TOKEN=rahasia`.

---

### B. Dengan Build (1 binary statis ~6 MB, tidak perlu Go saat dijalankan)
Cocok untuk pemakaian harian atau distribusi ke laptop/server lain:

```sh
# Build binary untuk mesin ini -> menghasilkan ./bin/omb
make build

# Atau build manual tanpa Makefile:
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/omb ./cmd/omb

# Jalankan binary hasil build:
./bin/omb
```

Cross-compile untuk OS lain:
```sh
make dist   # membuat semua binary di folder dist/ (Linux, macOS, Windows amd64/arm64)
make win    # membuat dist/openmausbot-windows-x64.zip siap klik-dua-kali
```

Memasang `omb` ke PATH sistem *(opsional)*:
```sh
# Linux / macOS
sudo cp bin/omb /usr/local/bin/omb
# lalu cukup ketik:
omb
```

---

## 3. Memasang CLI Agent & Model Gratis (untuk Chat UI)

Agar bot **Chief of Staff** bisa mengeksekusi script `desk/` dari chat, pilih salah satu CLI di bawah dan pasang di mesin Anda:

| Engine di UI | Perintah Instalasi | Catatan |
|---|---|---|
| **Gemini CLI** (`gemini-cli`) | `npm i -g @google/gemini-cli` lalu `gemini` (login Google) | Punya tier gratis harian |
| **OpenCode CLI** (`opencode`) | `curl -fsSL https://opencode.ai/install \| bash` | Mendukung model gratis Zen / Ollama |
| **Claude Code** (`claude`) | `npm i -g @anthropic-ai/claude-code` lalu `claude` | Butuh akun Anthropic |
| **Codex CLI** (`codex`) | `npm i -g @openai/codex` lalu `codex` | Butuh akun OpenAI |
| **Grok CLI** (`grok`) | Sesuai binary `grok` di PATH | Butuh API key xAI |

Jika hanya ingin chat **gratis tanpa CLI**, buka UI OpenMausBot → **⚙️ Pengaturan → 🎁 Model gratis**, lalu pilih **Ollama lokal** (`bash scripts/ollama-setup.sh`), **Google Gemini (tier gratis)**, **Groq**, atau **OpenRouter (`:free`)**.

---

## 4. Menjalankan 6-Agent Trading Desk (`desk/`)

Komponen `desk/` memakai **Python 3 stdlib murni** — bisa langsung dijalankan **tanpa build apa pun** dan **tanpa `pip install`** untuk Tahap 1 & Tahap 2.

### Langkah Awal
```sh
cp .env.example .env
chmod 600 .env
python3 desk/crew.py --doctor
```

### Tahap 1 — Demo Offline & `--mode live` (tetap `PAPER=True`)
```sh
# Jalankan 1 pass uji offline (demo)
python3 desk/crew.py --once --mode demo

# Jalankan 1 pass data pasar nyata (DexScreener + Rugcheck + Jupiter quote, tetap paper)
python3 desk/crew.py --once --mode live

# Jalankan dashboard live + background worker 6 agen (http://127.0.0.1:8788/?token=...)
python3 desk/dashboard.py --run-crew --mode live --port 8788
```
*(Buka URL `Auth URL` lengkap dengan `?token=...` yang dicetak di terminal.)*

### Tahap 2 — Dry-Run `simulateTransaction` via Solana RPC
Isi `SOLANA_RPC_URL` dan `SOLANA_PUBLIC_KEY` di `.env`, lalu jalankan:
```sh
python3 desk/crew.py --simulate <MINT_SOLANA> --usd 2.0
```

### Tahap 3 — Live On-Chain (Burner Wallet + Double-Lock)
1. Pasang library signing:
   ```sh
   pip install -r desk/requirements-live.txt
   ```
2. Isi di `.env`:
   - `DESK_EXECUTION_MODE=live`
   - `DESK_LIVE_CONFIRM=I_UNDERSTAND_REAL_MONEY`
   - `SOLANA_RPC_URL=https://<rpc-anda>`
   - `SOLANA_PRIVATE_KEY=<base58-atau-path-keypair-burner.json>`
   - `DESK_MAX_LIVE_TRADE_USD=5.0`
3. Verifikasi & jalankan:
   ```sh
   python3 desk/crew.py --doctor
   python3 desk/dashboard.py --run-crew --mode live --port 8788
   ```

---

## 5. Pengujian

```sh
# Unit test Python 6-Agent Desk (tanpa jaringan)
python3 -m unittest discover -s desk/tests -v

# Unit + API test Go OpenMausBot (butuh Go 1.24+)
make test
make vet
```
