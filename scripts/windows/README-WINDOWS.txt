OpenMausBot (Go build) - Windows x64
====================================

1. Ambil file: dist\omb-windows-amd64.exe
2. Rename jadi: omb.exe   (disarankan, supaya perintah di bawah pendek)
3. Taruh di folder pilihan, mis. C:\Tools\openmausbot\
4. Klik dua kali omb.exe  ->  browser terbuka sendiri di http://127.0.0.1:8799
   (atau jalankan omb.cmd kalau ingin jendela yang tidak langsung menutup saat error)

Kalau muncul "Windows protected your PC":
  More info -> Run anyway. Binary ini belum ditandatangani (code signing
  berbayar); isinya hanya Go + stdlib, tanpa DLL tambahan.

Kalau port 8799 sedang dipakai:
  omb.exe --port 8800

Data disimpan di:  %USERPROFILE%\.openmausbot-go\
  config.json      daftar bot & setelan
  providers.json   provider + API key (0600)
  threads\         transkrip percakapan (NDJSON)

Cek kondisi kapan saja:
  omb.exe doctor

Isi API key dari Command Prompt:
  echo sk-... | omb.exe key openai -

Memakai Ollama di Windows:
  winget install Ollama.Ollama     (atau unduh dari ollama.com/download)
  ollama serve
  ollama pull llama3.2:1b
  lalu di UI: Settings -> Provider -> Ollama -> Uji

Menutup aplikasi:
  Ctrl+C di jendela console. Data tetap tersimpan.

Catatan: Windows bisa menanyakan izin firewall saat pertama jalan, karena
server mendengarkan di 127.0.0.1. Menolak izin tetap bisa dipakai untuk
pemakaian lokal (loopback tidak diblokir), izin hanya perlu kalau kamu
memakai --bind 0.0.0.0 untuk diakses dari HP.
