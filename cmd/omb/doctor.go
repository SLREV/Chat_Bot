package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/provider"
)

// doctorCommand prints one screen of "what works, what does not, and the exact
// command that fixes it" — the thing a person needs on a fresh laptop before
// they understand the app.
func doctorCommand() int { return doctorRun(false) }

// doctorTry is `omb check`: same report, plus one real request per provider so
// the answer is "this one answers" instead of "this one has a key".
func doctorTry() int { return doctorRun(true) }

func doctorRun(try bool) int {
	fmt.Printf("OpenMausBot doctor · %s/%s · %s\n\n", runtime.GOOS, runtime.GOARCH, runtime.Version())

	problems := 0
	fatal := 0

	// 1. data directory
	dir := config.DataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		line("✗", "data dir", dir+" tidak bisa dibuat: "+err.Error())
		problems++
		fatal++
	} else {
		probe := filepath.Join(dir, ".probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			line("✗", "data dir", dir+" tidak bisa ditulis: "+err.Error())
			problems++
			fatal++
		} else {
			_ = os.Remove(probe)
			line("✓", "data dir", dir)
		}
	}

	// 2. port & 3. agent CLIs
	cfg, err := config.Load()
	if err != nil {
		line("✗", "config", err.Error())
		problems++
		fatal++
	} else {
		if free, detail := portFree(cfg.BindAddr, cfg.Port); free {
			line("✓", "port", fmt.Sprintf("%s:%d siap dipakai", cfg.BindAddr, cfg.Port))
		} else {
			line("!", "port", fmt.Sprintf("%s:%d %s — pakai --port %d", cfg.BindAddr, cfg.Port, detail, cfg.Port+1))
		}

		for _, e := range cfg.EnginesSnapshot() {
			if e.CLI == "" {
				continue
			}
			if path, err := exec.LookPath(e.CLI); err == nil {
				line("✓", "engine "+e.ID, path)
			} else {
				line("·", "engine "+e.ID, "tidak terpasang (opsional; pakai provider API key sebagai gantinya)")
			}
		}
	}

	// 4. providers, including a live probe for local endpoints
	fmt.Println()
	provs, err := config.LoadProviders()
	if err != nil {
		line("✗", "providers", err.Error())
		return 1
	}
	for _, p := range provs.List() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		ok, note := provider.Probe(ctx, p)
		cancel()

		state, env := config.KeyStatus(p)
		usedBy := usedByBot(cfg, p.ID)
		switch {
		case state == config.KeyEnvEmpty && usedBy != "":
			line("✗", p.Name, "dipakai bot \""+usedBy+"\" tapi $"+env+" kosong — tempel key di Settings → Provider, atau export "+env+"=... di shell yang menjalankan omb")
			problems++
		case state == config.KeyEnvEmpty:
			line("·", p.Name, "opsional — $"+env+" kosong. Aktifkan: export "+env+"=... atau omb key "+p.ID+" -")
		case state == config.KeyMissing && usedBy != "":
			line("✗", p.Name, "dipakai bot \""+usedBy+"\" tapi belum ada API key — omb key "+p.ID+" -")
			problems++
		case state == config.KeyMissing:
			line("·", p.Name, "opsional — belum ada API key. Aktifkan: omb key "+p.ID+" -")
		case ok && env != "":
			line("✓", p.Name, "key dari $"+env)
		case ok:
			line("✓", p.Name, p.BaseURL)
		default:
			line("✗", p.Name, note)
			problems++
		}
	}

	if try {
		if len(liveProbe(provs)) > 0 {
			problems = fatal // ada jalur yang hidup; hanya masalah fatal (data dir/config) yang tetap gagal
		}
	}

	fmt.Println()
	if problems == 0 && try {
		fmt.Println("Ada jalur yang hidup — cukup untuk mulai. Jalankan: omb")
		return 0
	}
	if problems == 0 {
		fmt.Println("Semua siap. Jalankan: omb")
		return 0
	}
	_ = try
	fmt.Printf("%d hal perlu perhatian. Yang paling umum:\n", problems)
	fmt.Println("  • Ollama: `ollama serve` lalu `ollama pull llama3.2:1b`")
	fmt.Println("  • Key cloud: `omb key openai -` (tempel key, Enter, Ctrl-D)")
	fmt.Println("  • Untuk mencoba tanpa model: pakai bot dengan engine \"Demo (offline)\"")
	return 1
}

// usedByBot names the first bot wired to a provider, so doctor can tell
// "optional, not set up" apart from "this bot will fail".
// liveProbe sends one tiny request per usable provider and prints which ones
// actually answer — the only test that reflects what a chat will do.
func liveProbe(provs *config.Providers) []string {
	fmt.Println()
	fmt.Println("Uji permintaan sungguhan (max_tokens=8):")
	alive := []string{}
	aliveIDs := []string{}
	for _, p := range provs.List() {
		state, _ := config.KeyStatus(p)
		if state == config.KeyEnvEmpty || state == config.KeyMissing {
			line("·", p.Name, "dilewati — belum ada key")
			continue
		}
		model := p.DefaultModel
		if len(p.Models) > 0 && model == "" {
			model = p.Models[0]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		t0 := time.Now()
		err := provider.VerifyModel(ctx, p, model)
		cancel()
		if err == nil {
			line("✓", p.Name, fmt.Sprintf("menjawab dalam %d ms (%s)", time.Since(t0).Milliseconds(), model))
			alive = append(alive, p.Name)
			aliveIDs = append(aliveIDs, p.ID)
			continue
		}
		var mnf *provider.ModelNotFoundError
		if errors.As(err, &mnf) {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
			models, lerr := provider.ListModels(ctx2, p)
			cancel2()
			if lerr != nil || len(models) == 0 {
				line("✗", p.Name, "model `"+model+"` tidak ada DAN daftar model tidak bisa diambil — periksa Base URL: "+p.BaseURL)
				continue
			}
			if len(models) > 0 {
				fixed := false
				for _, m := range provider.SortForChat(models) {
					ctx3, cancel3 := context.WithTimeout(context.Background(), 25*time.Second)
					e3 := provider.VerifyModel(ctx3, p, m)
					cancel3()
					if e3 == nil {
						fmt.Printf("  ↻ %-20s %s diganti %s\n", p.Name, model, m)
						if pr, ok := provs.ByID(p.ID); ok {
							pr.Models = provider.SortForChat(models)
							pr.ModelsSource = "live"
							pr.DefaultModel = m
							_ = provs.Upsert(pr)
						}
						line("✓", p.Name, "menjawab dengan "+m+" (model lama sudah tidak ada)")
						alive = append(alive, p.Name)
						aliveIDs = append(aliveIDs, p.ID)
						fixed = true
						break
					}
				}
				if fixed {
					continue
				}
			}
		}
		line("✗", p.Name, shortNote(err))
	}
	fmt.Println()
	if len(alive) == 0 {
		fmt.Println("Tidak ada provider yang menjawab. Cara tercepat supaya bisa chat hari ini:")
		fmt.Println("  1. Gratis tanpa key: `bash scripts/ollama-setup.sh` lalu `ollama pull llama3.2:1b`")
		fmt.Println("  2. Gratis dengan key: console.groq.com/keys atau aistudio.google.com/apikey")
		fmt.Println("     lalu `omb key groq -` (tempel key, Enter, Ctrl-D)")
		return alive
	}
	fmt.Println("Yang menjawab: " + strings.Join(alive, ", ") + " — pakai salah satunya di Settings → 🎁 Model gratis.")
	if len(aliveIDs) == 1 {
		fmt.Printf("Untuk memakai %s sekarang: `omb use %s`\n", alive[0], aliveIDs[0])
	}
	return alive
}

func shortNote(err error) string {
	var mnf *provider.ModelNotFoundError
	switch {
	case errors.As(err, &mnf):
		return "model `" + mnf.Model + "` tidak ada di akun ini — klik ⟳ Sinkron"
	case errors.Is(err, provider.ErrNoKey):
		return "API key tidak terbaca — omb key <provider> -"
	case errors.Is(err, provider.ErrUnreachable):
		return "tidak bisa dihubungi — cek Base URL / server lokal"
	}
	msg := err.Error()
	if r := []rune(msg); len(r) > 110 {
		msg = string(r[:110]) + "…"
	}
	return msg
}

func usedByBot(cfg *config.Settings, providerID string) string {
	if cfg == nil {
		return ""
	}
	for _, b := range cfg.Snapshot() {
		if b.Engine == providerID {
			return b.Name
		}
	}
	return ""
}

func line(mark, label, detail string) {
	fmt.Printf("  %s %-22s %s\n", mark, label, detail)
}

func portFree(bind string, port int) (bool, string) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err == nil {
		_ = ln.Close()
		return true, ""
	}
	dialHost := bind
	switch dialHost {
	case "", "0.0.0.0":
		dialHost = "127.0.0.1"
	case "::":
		dialHost = "::1"
	}
	// Something else is using it — is it us?
	conn, derr := net.DialTimeout("tcp", net.JoinHostPort(dialHost, strconv.Itoa(port)), 500*time.Millisecond)
	if derr == nil {
		_ = conn.Close()
		return false, "sudah dipakai (mungkin OpenMausBot yang lain sudah jalan)"
	}
	return false, "tidak bisa dibuka: " + err.Error()
}
