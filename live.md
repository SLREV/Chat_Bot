
---Cara Menjalankan Bertahap---
------------------------------------------------------------
Tahap 1 (--mode live, tetap PAPER=True):

Bash
python3 desk/crew.py --doctor
python3 desk/dashboard.py --run-crew --mode live --port 8788
----------------------------------------------------------------
Tahap 2 (Dry-run simulateTransaction via RPC tanpa private key):
Bash

export SOLANA_RPC_URL="https://api.mainnet-beta.solana.com"
python3 desk/crew.py --simulate <MINT_SOLANA> --usd 2.0 --pubkey <ALAMAT_WALLET>
------------------------------------------------------------------
Tahap 3 (Live burner wallet):
Bash

pip install -r desk/requirements-live.txt
export DESK_EXECUTION_MODE=live
export DESK_LIVE_CONFIRM=I_UNDERSTAND_REAL_MONEY
export SOLANA_RPC_URL="https://<rpc-anda>"
export SOLANA_PRIVATE_KEY="<base58-atau-path-keypair.json>"
export DESK_MAX_LIVE_TRADE_USD=5.0
python3 desk/crew.py --doctor
--------------------------------------------------------------------


-----Contoh Chat / Prompt yang Tinggal Anda Ketik------
Cek kesiapan Tahap 1–3:
"Cek kesiapan desk sekarang pakai python3 crew.py --doctor dan laporkan hasilnya."

Cek status & posisi terbuka:
"Status desk sekarang? Baca data/state.json dan 20 baris terakhir data/desk_log.jsonl."

Jalankan 1 putaran scan pasar nyata (masih paper):
"Jalankan 1 pass mode live (python3 crew.py --once --mode live) lalu laporkan hasil 6 agen."

Uji dry-run simulateTransaction (Tahap 2):
"Uji simulasi swap untuk token <MINT> $2 pakai crew.py --simulate."

Rem darurat & lanjut lagi:
"Stop semua entry baru sekarang." / "Resume trading desk."

Nyalakan dashboard pantau:
"Jalankan dashboard background dengan --run-crew --mode live dan kasih saya URL tokennya."
