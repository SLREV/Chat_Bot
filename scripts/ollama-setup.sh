#!/usr/bin/env bash
# ollama-setup.sh — pasang & siapkan Ollama untuk OpenMausBot (Go build)
#
# Kenapa bukan snap: paket snap menjalankan Ollama di dalam sandbox snapd, model
# tersimpan di ~/snap/ollama/... dan port bisa terikat berbeda. Skrip ini memakai
# installer resmi (binary + systemd unit) yang port-nya persis 127.0.0.1:11434 —
# alamat yang dipakai OpenMausBot secara default.
#
# Pakai:
#   bash scripts/ollama-setup.sh          # cek dulu, tanya sebelum mengubah apa pun
#   bash scripts/ollama-setup.sh --yes    # tanpa konfirmasi (untuk provisioning)
#   bash scripts/ollama-setup.sh --dry-run
#
# Aman dijalankan berulang: kalau Ollama sudah ada, bagian instal dilewati.

set -euo pipefail

ASSUME_YES=0
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --yes|-y) ASSUME_YES=1 ;;
    --dry-run) DRY_RUN=1 ;;
    --help|-h)
      sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "argumen tidak dikenal: $arg (pakai --help)" >&2; exit 2 ;;
  esac
done

c_ok()   { printf '\033[32m✓\033[0m %s\n' "$*"; }
c_warn() { printf '\033[33m!\033[0m %s\n' "$*"; }
c_err()  { printf '\033[31m✗\033[0m %s\n' "$*"; }
c_step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

run() {
  if [ "$DRY_RUN" = 1 ]; then
    echo "  [dry-run] $*"
    return 0
  fi
  if [ "$ASSUME_YES" = 1 ]; then
    "$@"
    return
  fi
  printf '  jalankan: %s\n  lanjut? [Y/n] ' "$*"
  read -r answer </dev/tty || answer="n"
  case "${answer:-y}" in
    [Yy]*|"") "$@" ;;
    *) c_warn "dilewati: $*"; return 1 ;;
  esac
}

ENDPOINT="http://127.0.0.1:11434"
BASE="$ENDPOINT/v1"

c_step "1/5 · lingkungan"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  OK_ARCH=1 ;;
  aarch64|arm64) OK_ARCH=1 ;;
  *) OK_ARCH=0 ;;
esac
[ "$OK_ARCH" = 1 ] && c_ok "arsitektur $ARCH didukung" \
  || c_warn "arsitektur $ARCH mungkin tidak punya binary resmi; pertimbangkan llama.cpp"

RAM_MB=0
if [ -r /proc/meminfo ]; then
  RAM_MB=$(awk '/MemTotal/{printf "%d", $2/1024}' /proc/meminfo)
  c_ok "RAM terdeteksi: ${RAM_MB} MB"
fi

# Model dipilih dari RAM total: sisakan ruang untuk OS, browser dan OpenMausBot.
if   [ "$RAM_MB" -le 4096 ]; then MODEL="llama3.2:1b";  NOTE="1B (~1,3 GB) — aman untuk 4 GB"
elif [ "$RAM_MB" -le 8192 ]; then MODEL="llama3.2";     NOTE="3B (~2 GB) — pas untuk 4–8 GB"
else                              MODEL="qwen2.5:7b";   NOTE="7B (~4,7 GB) — butuh ≥8 GB"
fi
c_ok "model yang disarankan: $MODEL ($NOTE)"

if ! command -v curl >/dev/null 2>&1; then
  c_err "curl belum ada. Pasang dulu: sudo apt install -y curl"
  exit 1
fi

c_step "2/5 · Ollama"
if command -v ollama >/dev/null 2>&1; then
  c_ok "sudah terpasang: $(ollama --version 2>/dev/null | head -1)"
else
  c_warn "ollama tidak ditemukan"
  echo "  Installer resmi: https://ollama.com/install.sh (binary ke /usr/local/bin + unit systemd)."
  echo "  Alternatif tanpa root: unduh manual dari https://github.com/ollama/ollama/releases"
  run sh -c 'curl -fsSL https://ollama.com/install.sh | sh' || {
    c_err "instalasi tidak selesai. Coba manual:"
    echo "    curl -fsSL https://ollama.com/install.sh -o /tmp/ollama.sh && less /tmp/ollama.sh && sudo sh /tmp/ollama.sh"
    exit 1
  }
fi

c_step "3/5 · server"
if curl -fsS --max-time 2 "$BASE/models" >/dev/null 2>&1; then
  c_ok "sudah jalan di $ENDPOINT"
else
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q '^ollama.service'; then
    run sudo systemctl enable --now ollama || true
  else
    c_warn "tidak ada unit systemd; menjalankan di latar belakang"
    if [ "$DRY_RUN" = 1 ]; then
      echo "  [dry-run] nohup ollama serve >/tmp/ollama.log 2>&1 &"
    else
      nohup ollama serve >/tmp/ollama.log 2>&1 &
      echo $! >/tmp/ollama.pid
    fi
  fi

  if [ "$DRY_RUN" = 1 ]; then
    c_ok "langkah server disimulasikan"
    DRY_SKIP_SERVER=1
  else
  printf '  menunggu %s' "$ENDPOINT"
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 2 "$BASE/models" >/dev/null 2>&1; then break; fi
    printf '.'; sleep 1
  done
  printf '\n'
  if curl -fsS --max-time 2 "$BASE/models" >/dev/null 2>&1; then
    c_ok "server siap"
  else
    c_err "server belum menjawab. Lihat log: journalctl -u ollama -n 50 --no-pager   (atau /tmp/ollama.log)"
    c_warn "kalau ini VPS/container: OLLAMA_HOST=127.0.0.1:11434 ollama serve"
    exit 1
  fi
  fi
fi

c_step "4/5 · model"
if [ "$DRY_RUN" = 1 ]; then
  echo "  [dry-run] ollama pull $MODEL"
  c_ok "semua langkah tersimulasi. Jalankan tanpa --dry-run untuk benar-benar memasang."
  exit 0
fi
if command -v ollama >/dev/null 2>&1 && ollama list 2>/dev/null | awk 'NR>1{print $1}' | grep -qx "$MODEL"; then
  c_ok "$MODEL sudah ada"
else
  run ollama pull "$MODEL" || c_warn "pull dilewati — jalankan sendiri: ollama pull $MODEL"
fi

c_step "5/5 · verifikasi"
if [ "$DRY_RUN" = 1 ]; then
  echo "  [dry-run] curl -s $BASE/models"
  exit 0
fi
if curl -fsS "$BASE/models" | grep -q '"id"'; then
  c_ok "endpoint OpenAI-compatible menjawab di $BASE"
else
  c_err "endpoint tidak mengembalikan daftar model"
  exit 1
fi

cat <<EOF

Selesai. Langkah berikutnya di OpenMausBot:

  1. jalankan:  omb        (atau ./bin/omb)
  2. Settings → Provider → Ollama → "Uji"  → harus muncul ✓
  3. Edit bot → Engine = Ollama → Model = $MODEL
  4. kirim pesan

Setara lewat terminal:
  omb provider openai $BASE $MODEL "Ollama lokal"

Catatan RAM: kalau laptop mulai swap, pakai model lebih kecil
  ollama pull llama3.2:1b     lalu pilih di dialog bot
EOF
