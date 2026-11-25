# OpenMausBot · Go build

Versi **Go** dari [OpenMausBot](https://github.com/milind-soni/OpenMausBot) — dibuat ulang supaya ringan
di laptop 4 GB RAM. Bukan port satu-satu: fitur yang berat (Electron, React, VM cloud, marketplace
Composio) dibuang, dan intinya dipertahankan — **daftar bot seperti aplikasi chat, tiap bot punya
engine, model, dan kepribadian sendiri, dengan jawaban yang di-stream**.

| | Build asli (repo upstream) | Build Go ini |
|---|---|---|
| Runtime | Electron + Node 24 + React + Vite | **satu binary Go, tanpa runtime** |
| Ukuran | instalasi ratusan MB | **6,0 MB** (stripped, UI ter-embed) |
| RAM idle | ~400–700 MB | **~7 MB** (VmRSS, 8 thread) |
| Waktu start | beberapa detik | **< 100 ms** |
| Dependency | `pnpm install` (ratusan paket) | **nol** — hanya stdlib Go |
| Data | `~/.openmausbot` | `~/.openmausbot-go` (JSON + NDJSON) |

Angka RAM di atas diukur di sandbox Linux x64 dengan binary `dist/omb-linux-amd64`.

## Provider API key (tanpa CLI)

Selain CLI lokal, bot bisa memakai **API key** langsung ke vendor. Dua bentuk yang didukung:

| Kind | Endpoint | Dipakai oleh |
|---|---|---|
| `openai` | `POST {base}/chat/completions` (SSE) | OpenAI, OpenRouter, Groq, DeepSeek, Ollama, LM Studio, vLLM, apa pun yang OpenAI-compatible |
| `anthropic` | `POST {base}/messages` (SSE) | Anthropic API key (`x-api-key`) |

Sudah terisi otomatis saat pertama jalan: OpenAI, Anthropic, OpenRouter, Groq, DeepSeek, dan
**Ollama lokal** (tanpa key). Key boleh berupa teks **atau** nama env var (`$OPENAI_API_KEY`), jadi
tidak wajib menyimpan key di disk sama sekali.

### Lewat UI

**⚙️ Settings → 🔑 Provider API key**:

1. Tempel key di kolom provider → **Simpan** (key tidak pernah dikirim balik ke browser, hanya
   `••••1234` yang tampil).
2. **Uji** → satu permintaan kecil; hasilnya `✓ model menjawab dalam N ms` atau pesan error yang
   menyebut penyebabnya (401 key salah, 404 model/endpoint salah, 429 kuota, koneksi ditolak).
3. **Model** → daftar model dari `GET /models` provider.
4. **＋ Tambah** → provider lain: `kind`, Base URL, model default, key.
5. Lalu **✏️ Edit bot → Engine/Provider** → pilih `🔑 Nama Provider`, isi model. Sidebar menandai
   bot dengan 🔑 (API), 🖥 (CLI), atau 🧪 (demo).

### Lewat CLI (server/VPS, tanpa browser)

```sh
omb doctor                                # cek data dir, port, CLI, key, dan endpoint lokal
omb keys                                  # daftar provider + status key (bukan key-nya)
echo "$OPENAI_API_KEY" | omb key openai - # simpan key dari stdin (tidak masuk history)
omb key anthropic-api sk-ant-...          # atau langsung sebagai argumen
omb provider openai http://127.0.0.1:11434/v1 llama3.2 "Ollama lokal"
omb --open=false                          # jalankan headless
```

### Keamanan key

- Disimpan di **`providers.json`** (mode `0600`) di folder data — **terpisah** dari `config.json`,
  jadi `cat config.json` tidak pernah membocorkan key.
- Endpoint mengembalikan bentuk `MaskedProvider`: tanpa field key, hanya `hasKey` + `keyHint`.
- Tidak masuk ke transkrip, tidak masuk ke log; pesan error dari vendor dipotong dan tidak memuat
  key.
- Mengedit model **tidak** menghapus key; clear eksplisit: `{"apiKey":"__clear__"}`.
- Variabel env yang kosong menghasilkan error yang menyebut nama variabelnya, bukan request tanpa
  kredensial.

### Kalau SEMUA provider kehabisan saldo

Ini kasus nyata yang sudah ditangani build ini. Tiga lapis pertahanan:

1. **Pesan per-vendor** (bukan asal tunjuk): `DeepSeek: saldo akun habis` dengan tautan top-up DeepSeek
   (`platform.deepseek.com/top_up`), bukan tautan OpenRouter. Vendor yang punya jalur gratis
   menyebutkannya (OpenRouter `:free`, Groq tier gratis, Gemini tier gratis); vendor yang tidak
   (DeepSeek, OpenAI, Anthropic) mengatakannya terus terang.
2. **Fallback berantai** — kalau provider sebuah bot gagal karena **saldo habis / key kosong / endpoint
   mati**, bot otomatis mencoba provider lain yang aktif dengan urutan `fallback` (0 = tidak dipakai,
   kecil = dicoba lebih dulu). Transkrip mencatat siapa yang menjawab:
   `ℹ (dialihkan ke Ollama lokal karena provider utama tidak bisa menjawab)`.
   Bawaan: Gemini (5) → Groq (10) → OpenRouter (20) → OpenAI (30) → DeepSeek (40) → Anthropic (50) →
   Ollama lokal (90, gratis, butuh `ollama serve`).
3. **Model `:free` tidak bocor** lagi: `:free` hanya konvensi OpenRouter, jadi tidak pernah dikirim ke
   DeepSeek/OpenAI (dulu muncul `model deepseek-chat-v3.1:free tidak ada di DeepSeek`).

Jalur gratis yang benar-benar bisa dipakai sekarang:

| Jalur | Butuh | Catatan |
|---|---|---|
| **Ollama lokal** | `ollama serve` + `ollama pull llama3.2:1b` | gratis penuh, offline, di rantai paling akhir |
| **Google Gemini** | key dari `aistudio.google.com/apikey` | ada tier gratis, tanpa kartu kredit (provider baru di build ini) |
| **Groq** | key dari `console.groq.com/keys` | kuota gratis harian |
| **OpenRouter `:free`** | key OpenRouter | saldo 0 tetap jalan, kena rate limit |

Urutan cadangan bisa diubah per provider: Settings → Provider → `urutan cadangan (fallback): [90] Set`.

### Hapus API key vs hapus provider

Dua hal berbeda, dua tombol berbeda:

| Tombol | Yang terjadi | Provider tetap ada? |
|---|---|---|
| **🗑 Hapus key** | hanya kredensialnya yang dibuang; nama, Base URL, daftar model, urutan cadangan tetap | ✅ ya |
| **Hapus provider** | provider hilang dari daftar (konfirmasi menyebutkan bahwa ini bukan cara membuang key) | ❌ tidak |
| **↺ Pulihkan bawaan** | mengembalikan provider bawaan yang terlanjur dihapus (Groq, Gemini, OpenAI, …) **tanpa** menyentuh key yang sudah tersimpan | — |

Lewat API: `DELETE /api/providers/{id}/key` (buang key), `DELETE /api/providers/{id}` (buang
provider), `POST /api/providers/restore` (pulihkan bawaan).

### "Semua provider gagal" — apa yang harus dilihat

Jawaban singkat: jalankan **`omb check`**. Perintah ini mengirim **satu permintaan sungguhan**
(`max_tokens=8`) ke setiap provider yang punya key, jadi hasilnya "provider ini menjawab", bukan
"provider ini punya key". Kalau model yang dipakai sudah dipensiunkan, `omb check` sekaligus
mengambil daftar model baru dan menyimpannya.

```
$ omb check
Uji permintaan sungguhan (max_tokens=8):
  · OpenAI       dilewati — belum ada key
  ✗ Ollama       tidak bisa dihubungi — cek Base URL / server lokal
  ✗ Groq         model `llama-3.1-8b-instant` tidak ada di akun ini — klik ⟳ Sinkron
  ✓ Gemini       menjawab dalam 340 ms (gemini-2.0-flash)

Yang menjawab: Gemini — pakai salah satunya di Settings → 🎁 Model gratis.
Untuk memakai Gemini sekarang: `omb use gemini`
```

Lalu `omb use gemini` mengarahkan **semua** bot ke jalur yang hidup, tanpa membuka browser.

Kalau di dalam chat semuanya gagal, balasannya sekarang memuat **alasan setiap provider** sekaligus,
bukan hanya yang pertama:

```
⚠️ Maus: semua provider gagal (4 dicoba):
• Groq: model `llama-3.1-8b-instant` tidak ada di akun ini
• OpenRouter: saldo habis
• Ollama: tidak bisa dihubungi (server mati / Base URL salah)
• Gemini: API key belum ada
```

Tiga penyebab paling sering, berurutan:

1. **Key tidak ada / env kosong** — `omb keys` menampilkan status tiap provider (`ok`, `missing`,
   `env_empty`). Perbaiki: `omb key groq -` lalu tempel key.
2. **Model sudah dipensiunkan** — `omb check` memperbaikinya sendiri, atau klik **⟳ Sinkron**.
3. **Ollama tidak jalan** — `ollama serve`, lalu `ollama pull llama3.2:1b`.

### Model mana yang gratis?

Tergantung vendornya — dan aplikasi sekarang menampilkannya sendiri, bukan menebak:

| Provider | Jenis gratis | Model gratis |
|---|---|---|
| **Ollama (lokal)** | gratis penuh | semua model yang kamu `pull` (`llama3.2:1b`, `llama3.2`, `qwen2.5`, `mistral`) |
| **Groq** | tier gratis (rate limit) | seluruh katalog Groq: `llama-3.1-8b-instant`, `openai/gpt-oss-20b`, dll. |
| **Google Gemini** | tier gratis | `gemini-2.0-flash`, `gemini-2.0-flash-lite`, `gemini-1.5-flash` |
| **OpenRouter** | hanya akhiran `:free` | `deepseek/deepseek-chat-v3.1:free`, `meta-llama/llama-3.3-70b-instruct:free`, `google/gemini-2.0-flash-exp:free` |
| **DeepSeek / OpenAI / Anthropic** | **tidak ada** | — (harus isi saldo) |

Cara memakainya: **Settings → 🎁 Model gratis** → klik nama model → bot yang sedang aktif langsung
dipakai model itu. Endpoint: `GET /api/free-models` dan `POST /api/providers/{id}/use-free` (dengan
`{"botId": "…"}` atau tanpa untuk mengubah default provider).

Daftar gratis diambil dari **daftar model provider itu sendiri** (hasil ⟳ Sinkron bila ada), jadi
model yang sudah dipensiunkan tidak akan muncul. DeepSeek/OpenAI/Anthropic sengaja **tidak**
ditampilkan karena memang tidak punya jalur gratis — aplikasi tidak mengarang nama model.

Saran untuk laptop 4 GB tanpa saldo: `bash scripts/ollama-setup.sh` lalu
**Settings → 🎁 Model gratis → Ollama → llama3.2:1b** (≈1,3 GB RAM, jalan offline).

### Model dipensiunkan vendor (Groq 404 "does not exist")

Groq (dan vendor lain) rutin mempensiunkan model — `llama-3.3-70b-versatile` pernah hilang dari
daftar. Dulu itu berarti bot berhenti total. Sekarang aplikasi menyembuhkan dirinya:

1. 404 yang **menyebut nama model** dikenali sebagai `ModelNotFoundError` (404 biasa — Base URL salah —
   tidak diperlakukan sama).
2. Daftar model **disinkronkan ulang dari API vendor** dan ditandai `live`.
3. Permintaan **diulang sekali** dengan model pertama yang benar-benar ada.
4. Transkrip mencatat dan **setelan bot ikut diperbarui**:
   `ℹ (model llama-3.3-70b-versatile sudah tidak tersedia di Groq; dipakai llama-3.1-8b-instant — setelan bot ikut diperbarui)`

Kalau model pertama di daftar baru juga tidak ada (Groq memensiunkan beberapa sekaligus), aplikasi
**mencoba terus ke model berikutnya** sampai ada yang menjawab — dan melewati model yang bukan untuk
chat (`whisper-*`, `*-guard`, `*embed*`), karena Groq menaruh semuanya di endpoint `/models` yang sama.
Transkrip mencatat model yang **benar-benar dipakai**, bukan model yang gagal.

Kalau ternyata tidak ada satu pun model yang bisa dipakai, balasannya menyebut model yang hilang,
menegaskan bahwa mengganti API key tidak akan menolong, dan menampilkan daftar model yang benar-benar
ada di akun itu:

```
🧩 Groq tidak menyediakan model `llama-3.1-8b-instant`.
Bukan soal API key — mengganti key tidak akan mengubah apa pun.

Model yang benar-benar ada di Groq: `openai/gpt-oss-20b`, `qwen/qwen3-32b`, …
```

Untuk mencegahnya sejak awal, klik **⟳ Sinkron** di kartu provider Groq sekali — daftar model jadi
dari API, bukan tebakan bawaan.

### "model … tidak ada di X, dipakai …" — kenapa?

Karena **daftar model bawaan bisa usang**, dan sebelumnya build ini terlalu percaya diri memakainya.
Nama seperti "DeepSeek V4.1 Flash" adalah **nama tampilan di UI vendor**; id yang dipakai API tetap
`deepseek-chat`. Build ini dulu membandingkan dengan daftar bawaan lalu diam-diam mengganti model.

Aturan sekarang:

| Sumber daftar model | Perilaku |
|---|---|
| **bawaan / `seed`** (tebakan) | model yang kamu tulis **dipakai apa adanya** — vendor yang menilai; tidak ada penggantian senyap |
| **dari API / `live`** (setelah ⟳ Sinkron) | nama yang tidak ada di daftar memang tidak ada → fallback ke default dengan catatan |

Cara memastikannya:

1. Settings → Provider → kartu provider → **⟳ Sinkron** — mengambil `GET /models` asli. Kartu lalu
   menampilkan `daftar model: dari API` + jumlah model.
2. Dialog bot → **✓ Uji model ini** — mengirim satu permintaan mini dengan nama model persis seperti
   yang kamu tulis, dan menampilkan jawaban vendor (mis. `✗ Model Not Exist` atau `✓ … menjawab (312 ms)`).
3. Pakai id API, bukan nama tampilan: `deepseek-chat`, `deepseek-reasoner`; bukan "DeepSeek V4.1 Flash".

### Kalau muncul 402 (kredit habis)

`HTTP 402 — This request requires more credits, or fewer max_tokens … You requested up to 2048 tokens,
but can only afford 980` artinya saldo OpenRouter lebih kecil dari `max_tokens` yang diminta.
Build ini menanganinya, bukan cuma menampilkannya:

1. **Retry otomatis** dengan budget yang mampu dibayar (dibatasi 512 token, minimum 64) — bot tetap
   menjawab, hanya lebih pendek. Transkrip mencatat: `ℹ (kredit habis: jawaban dipendekkan ke 512 token)`.
2. **`max_tokens` bisa diatur** per provider: Settings → Provider → baris `max_tokens: [2048] Set`.
   Nilai 0 = pakai default build; dibatasi 64–32000.
3. **Model `:free`** ditambahkan ke daftar OpenRouter (`deepseek/deepseek-chat-v3.1:free`,
   `meta-llama/llama-3.3-70b-instruct:free`, `google/gemini-2.0-flash-exp:free`) — jalan tanpa saldo,
   hanya kena rate limit.
4. Kalau tetap gagal, pesannya menyebut tiga pilihan konkret (model `:free`, turunkan `max_tokens`,
   atau isi saldo di `openrouter.ai/settings/credits`) — dan tautan kredit muncul langsung di kartu
   provider.

Diuji end-to-end dengan server tiruan OpenRouter: permintaan 2048 → 402 → ulang 512 → jawaban masuk
dengan catatan "dipendekkan ke 512 token".

### Kalau muncul "api key tidak tersedia: environment variable … kosong"

Artinya provider itu **membaca key dari environment variable, dan variabelnya kosong di proses `omb`
yang sedang jalan**. Ini kondisi yang paling sering menjebak karena reference-nya memang ada, jadi
kartu provider sempat terlihat "punya key".

Dua jalan keluar, pilih satu:

1. **Tempel key langsung** (disarankan): Settings → Provider → OpenAI → tempel → **Simpan**.
   Setelah itu provider tidak lagi bergantung pada env var.
2. **Set env var di shell yang menjalankan `omb`**, lalu restart:
   ```sh
   export OPENAI_API_KEY=sk-...
   omb
   ```
   `export` di terminal lain **tidak terbaca** oleh proses yang sudah jalan.

Status key sekarang jujur di tiga tempat:

| Tempat | Contoh |
|---|---|
| Kartu provider | `✗ $OPENAI_API_KEY kosong — key ini belum aktif` + `dipakai: Maus` |
| Sidebar bot | dot tidak hijau, label `perlu setup` |
| `omb doctor` | `✗ OpenAI  dipakai bot "Maus" tapi $OPENAI_API_KEY kosong — …` |

`/api/providers` juga mengembalikan `keyState` (`ok` / `env_empty` / `missing` / `not_needed`),
`envName`, dan `active` — jadi skrip bisa membedakan "belum diisi" dari "diisi tapi env-nya kosong".

### Kalau muncul 401 (key ditolak)

`401` hampir selalu soal **key-nya**, bukan aplikasinya. Yang dilakukan build ini:

| Masalah | Penanganan |
|---|---|
| Key di-paste dengan `"kutip"`, `Bearer `, spasi/baris baru | dibersihkan otomatis; UI memberi tahu apa yang dibuang (`"Bearer " dibuang`) |
| Key milik vendor lain (mis. `sk-proj-…` OpenAI diisi ke OpenRouter) | **diperingatkan sebelum dipakai**: "Key ini tampak milik OpenAI, bukan OpenRouter" — tetap disimpan, tapi kamu tahu penyebabnya |
| Vendor minta format tertentu | dicek: OpenRouter `sk-or-v1-…`, Anthropic `sk-ant-…`, Groq `gsk_…`, OpenAI `sk-…` |
| Pesan error 401 | menyebut URL tempat membuat key, mis. `buat/ganti key di https://openrouter.ai/keys` |

Contoh nyata (OpenRouter, `401 User not found`): key di dashboard OpenRouter sudah dihapus/di-rotate,
atau yang di-paste key milik akun lain. Buat key baru di **openrouter.ai/keys**, lalu
**Settings → Provider → OpenRouter → Uji**.

### Riwayat percakapan

Mode API mengirim maksimal **12 turn terakhir / ~12.000 karakter** sebagai konteks (`internal/agent`),
supaya thread panjang tidak membengkakkan context window maupun RAM di laptop 4 GB.

### Memasang Ollama (Kali/Debian/Ubuntu/macOS)

```sh
omb doctor                     # 1 layar: apa yang siap, apa yang belum, perintah perbaikannya
bash scripts/ollama-setup.sh   # 2 pasang + jalankan + tarik model yang pas untuk RAM-mu
```

`ollama-setup.sh` sadar-RAM: **≤4 GB → `llama3.2:1b`**, 4–8 GB → `llama3.2`, ≥8 GB → `qwen2.5:7b`.
Ia memakai installer resmi (bukan snap — snap mengurung model di `~/snap` dan port-nya bisa beda),
mendeteksi systemd, menunggu port 11434 benar-benar hidup, lalu memverifikasi
`/v1/models`. Aman diulang; ada `--dry-run` dan `--yes`.

Di Kali, `ollama` memang tidak ada di repo — pesan `sudo snap install ollama` yang muncul adalah
saran snapd, bukan satu-satunya jalan.

### Kalau muncul "koneksi ditolak" (Ollama/lokal)

Artinya endpoint lokalnya belum jalan — bukan bug aplikasi. Urutannya:

```sh
ollama serve            # atau buka aplikasi Ollama (Windows/macOS)
ollama pull llama3.2    # sekali saja per model
curl http://127.0.0.1:11434/v1/models   # harus menjawab JSON
```

Di UI, bot yang memakai provider lokal yang mati sekarang menampilkan **banner kuning** di atas chat
dengan perintah di atas (ada tombol *salin*) dan tombol **↻ Cek lagi** — jadi ketahuan sebelum
mengetik pesan, bukan sesudahnya.

Kalau provider-nya terjangkau tapi modelnya tidak ada, bot tidak lagi mengirim model asing ke
endpoint. `gpt-4o-mini` yang tertinggal saat bot dipindah ke Ollama otomatis diganti model default
provider itu, dan transkrip mencatat alasannya:

> ℹ (model gpt-4o-mini tidak ada di Ollama (lokal, tanpa key), dipakai llama3.2)

## Mengelola bot: ganti, duplikat, hapus

Klik kanan bot di sidebar (atau tekan lama di HP, atau tombol **⋯** di header chat):

| Menu | Akibat |
|---|---|
| ✏️ **Edit bot / ganti avatar** | ubah nama, avatar, engine/provider, model, kepribadian, folder kerja |
| 🧬 **Duplikat** | salinan dengan setelan sama + **avatar baru otomatis** |
| 🧹 **Bersihkan percakapan** | transkrip dihapus, bot tetap ada |
| 🔁 **Ganti dengan bot baru** | bot lama dihapus **dan** dialog bot baru langsung terbuka |
| 🗑 **Hapus bot** | bot + seluruh percakapannya hilang (ada konfirmasi) |

Lewat terminal:

```sh
curl -X DELETE http://127.0.0.1:8799/api/bots/<id>        # hapus
curl -X POST   http://127.0.0.1:8799/api/bots -d '{"name":"Baru"}' -H 'content-type: application/json'
```

### Avatar 3D

12 wajah clay/3D bawaan (maus, clover, ember, luna, pip, nori, mochi, rocky, zest, cocoa, frost,
raven) — masing-masing beda warna **dan** ekspresi mata/mulut. Semua ditanam sebagai data URI
(`internal/server/web/avatars/avatars.js`), jadi tetap **offline, tanpa CDN, tanpa file eksternal**.

- Bot baru otomatis dapat wajah yang belum dipakai (`Settings.NextAvatar()`), jadi dua bot tidak
  pernah kembar.
- Pilih lain di dialog bot: **Avatar 3D** → klik salah satu; pilih **🙂 emoji** untuk kembali ke emoji.
- Menambah wajah: edit `PALETTE` di `tools/avatar-src/make_avatars.py`, jalankan
  `python3 tools/avatar-src/make_avatars.py`, lalu tambahkan id barunya ke `config.AvatarIDs`.

## Isi fitur

- **Roster bot seperti aplikasi chat** — sidebar, avatar, pencarian, status sibuk/idle.
- **Engine per bot** — `claude`, `codex`, `grok`, atau engine apa pun yang mau kamu daftar di
  `config.json` (binary + args). Ada **engine demo offline** supaya aplikasi langsung bisa dipakai
  sebelum CLI agent terpasang.
- **Provider API key** — OpenAI/Anthropic/OpenRouter/Groq/DeepSeek/Ollama lewat HTTP + SSE, key
  disimpan terpisah (`providers.json`, 0600), tombol Uji key, daftar model live.
- **Streaming nyata** — delta teks dikirim lewat SSE saat engine menulis, bukan menunggu selesai.
- **Antrean per bot** — pesan kedua saat turn masih jalan akan mengantre (maks 8), jadi tidak ada
  tiga proses CLI lahir bersamaan di laptop kecil.
- **Stop turn** — membunuh seluruh process group CLI, bukan cuma wrapper-nya.
- **Transkrip NDJSON** — append-only, di-*tail*, dengan cache ring 200 pesan per thread, jadi RAM
  tidak tumbuh walau percakapan ribuan pesan.
- **UI mobile-friendly** — bisa dibuka dari HP di jaringan yang sama (`--bind 0.0.0.0`).

## Jalankan

```sh
# butuh Go 1.24+
make build          # → bin/omb (6 MB)
./bin/omb           # → http://127.0.0.1:8799, browser terbuka otomatis
```

Atau langsung tanpa build:

```sh
go run ./cmd/omb
```

Opsi yang sering dipakai:

```sh
./bin/omb --port 9000          # ganti port
./bin/omb --bind 0.0.0.0       # bisa diakses dari HP di WiFi yang sama
./bin/omb --open=false         # headless / server
./bin/omb --data /path/ke/dir  # pindahkan data
./bin/omb --low-mem=false      # matikan tuning GC (default: NYALA, heap cap 192 MB)
```

Binary untuk OS lain:

```sh
make dist   # dist/omb-{linux,macos,windows}-{amd64,arm64}
make win    # dist/openmausbot-windows-x64.zip (omb.exe + omb.cmd + README)
```

### Windows: .exe-nya bisa langsung jalan?

Bisa — **tanpa installer, tanpa Node, tanpa DLL tambahan**. `dist/omb-windows-amd64.exe` adalah
binary statis PE32+ x86-64 yang hanya mengimpor DLL bawaan Windows (`kernel32`, `ws2_32`, `crypt32`,
`dnsapi`, `bcryptprimitives`, `iphlpapi`, `mswsock`, `userenv`, …), tanpa dependensi Visual C++
Redistributable.

1. Rename `omb-windows-amd64.exe` → `omb.exe`, taruh di folder mana pun.
2. Klik dua kali → browser terbuka di `http://127.0.0.1:8799`.
3. Kalau muncul "Windows protected your PC": **More info → Run anyway** (binary belum
   code-signed; itu soal sertifikat berbayar, bukan isi program).

Yang perlu diketahui:

| Hal | Kenyataan |
|---|---|
| Arsitektur | x86-64 saja (Windows 10/11 64-bit). ARM64 Windows: jalankan `GOOS=windows GOARCH=arm64 go build` |
| Jendela console | tetap terbuka selama server hidup; tutup dengan Ctrl+C (data tersimpan) |
| Firewall | mungkin ditanya sekali; menolak tetap bisa dipakai lokal (loopback) |
| Data | `%USERPROFILE%\.openmausbot-go\` |
| Ollama | `winget install Ollama.Ollama` → `ollama pull llama3.2:1b` |

## Menyambungkan agent sungguhan

Aplikasi ini tidak memanggil API model sendiri: ia menjalankan CLI agent yang sudah kamu login.

```sh
npm i -g @anthropic-ai/claude-code     # atau: @openai/codex  /  grok CLI
claude                                  # login sekali
```

Lalu di UI: **✏️ Edit bot → Engine → Claude Code**. Kalau CLI tidak ada di PATH, bot menjawab dengan
pesan error yang jelas, bukan crash.

Engine bisa ditambah/diubah di `~/.openmausbot-go/config.json`:

```json
{
  "id": "ollama",
  "name": "Ollama lokal",
  "cli": "ollama",
  "args": ["run", "llama3.2"],
  "prompt": "stdin",
  "enabled": true
}
```

`prompt: "arg"` menaruh prompt sebagai argumen terakhir; `"stdin"` mengirimnya lewat stdin.
Field `workdir` pada bot membuat CLI dijalankan di folder proyek tertentu — berguna untuk agent
coding.

## Arsitektur

```
cmd/omb/main.go            flag, tuning RAM, sinyal, buka browser
cmd/omb/keys.go            subperintah `omb keys` / `omb key` / `omb provider`
cmd/omb/doctor.go          `omb doctor`: diagnosa 1 layar + perintah perbaikan
tools/avatar-src/          generator avatar 3D (Pillow) → avatars.js + PNG
scripts/ollama-setup.sh    pemasangan Ollama sadar-RAM (installer resmi, bukan snap)
internal/config            settings + roster bot (config.json) + provider & key (providers.json, 0600)
internal/store             transkrip NDJSON, ring 200 pesan, pub/sub, tail-reader
internal/engine            satu turn: proses CLI lokal ATAU provider HTTP (routing di sini)
internal/provider          klien OpenAI-compatible + Anthropic, SSE, error yang bisa ditindak
internal/agent             antrean per bot, satu goroutine per bot, status sibuk
internal/server            REST + SSE + UI ter-embed (//go:embed)
internal/server/web        index.html — vanilla JS/CSS, tanpa CDN, tanpa bundler
```

Tidak ada dependency eksternal Go. Itu disengaja: `go build` selalu berhasil offline, dan tidak ada
supply chain yang harus diaudit.

### Kenapa ringan

1. **Tidak ada Electron/Chromium** — UI-nya HTML biasa yang dibuka di browser yang sudah ada.
2. **Tidak ada Node** — harness-nya binary native; tidak ada GC V8, tidak ada `node_modules`.
3. **Ring buffer** — RAM tidak sebanding dengan panjang riwayat; file lama dibaca dari disk saat
   di-scroll.
4. **Heap cap + GC lebih agresif** — `debug.SetMemoryLimit(192MB)` dan `GCPercent(40)`; sisa RAM
   dipakai CLI agent, bukan UI.
5. **Satu worker per bot** — antrean membatasi proses CLI bersamaan.

## Tes

```sh
make test     # unit + API, tanpa jaringan, ~0,5 s
make vet
```

Yang diuji: round-trip transkrip, paginasi, batas ring, baris NDJSON yang sobek karena crash,
path traversal pada id bot, streaming engine, timeout yang membunuh child, error CLI hilang,
endpoint REST, lifecycle bot — plus jalur API key: streaming OpenAI & Anthropic, key dari env,
key salah (401), endpoint mati, timeout saat streaming, dan tes bahwa key **tidak pernah** muncul di
respons `/api/state`, `/api/providers`, maupun di `config.json`.

## Yang belum ada (dibanding upstream)

Sengaja keluar dari scope build ringan ini: tool-calling/agentic loop pada mode API (mode API
menjawab teks, belum menjalankan tool), cloud computer/VM, marketplace aplikasi (Composio),
voice/TTS, panggilan, pairing HP via relay, auto-update, kontrol desktop, dan multi-user/enterprise.
Semua itu butuh proses besar atau layanan pihak ketiga — persis yang membuat build aslinya berat.

Lisensi: Apache 2.0, mengikuti proyek asal. Proyek ini tidak berafiliasi dengan xAI, OpenAI, atau
Anthropic.
