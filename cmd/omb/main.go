// Command omb is the OpenMausBot harness in Go: one static binary that owns
// every agent process, serves the chat UI and streams events over SSE.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/agent"
	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/server"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "0.1.0"

func main() {
	// Sub-commands before flag parsing, so `omb keys` is not read as a flag.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "keys", "providers", "key", "provider", "use":
			os.Exit(keysCommand(os.Args[1:]))
		case "doctor":
			os.Exit(doctorCommand())
		case "check":
			os.Exit(doctorTry())
		case "help", "-h", "--help":
			usage()
			return
		}
	}

	var (
		port    = flag.Int("port", 0, "HTTP port (default: config, else 8799)")
		bind    = flag.String("bind", "", "bind address (default: config, else 127.0.0.1)")
		open    = flag.Bool("open", true, "open the UI in the default browser")
		dataDir = flag.String("data", "", "data directory (default ~/.openmausbot-go)")
		token   = flag.String("token", "", "API auth token (default: $OMB_TOKEN, else auto-generated)")
		showVer = flag.Bool("version", false, "print the version and exit")
		lowRAM  = flag.Bool("low-mem", true, "tune the Go GC for a 4 GB laptop")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("openmausbot-go", version, runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH)
		return
	}

	if *dataDir != "" {
		_ = os.Setenv("OMB_DATA_DIR", *dataDir)
	}
	if *lowRAM {
		// A soft memory limit keeps the heap from growing past what a 4 GB
		// machine can spare while a CLI agent runs next to us, and a gentler
		// GC target trades a little CPU for a much smaller resident set.
		debug.SetMemoryLimit(192 << 20)
		debug.SetGCPercent(40)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *bind != "" {
		cfg.BindAddr = *bind
	}

	provs, err := config.LoadProviders()
	if err != nil {
		log.Fatalf("providers: %v", err)
	}
	st, err := store.Open()
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	mgr := agent.NewManager(cfg, provs, st)
	srv := server.New(cfg, provs, st, mgr, cfg.BindAddr, cfg.Port)
	server.Version = version

	authToken := strings.TrimSpace(*token)
	autoToken := false
	if authToken == "" {
		authToken = strings.TrimSpace(os.Getenv("OMB_TOKEN"))
	}
	if authToken == "" {
		authToken = server.GenerateToken()
		autoToken = true
	}
	srv.SetAuthToken(authToken)

	// Fail fast with a readable message instead of a bare "address in use".
	if ln, err := net.Listen("tcp", net.JoinHostPort(cfg.BindAddr, strconv.Itoa(cfg.Port))); err != nil {
		log.Fatalf("port %d tidak bisa dipakai: %v\nCoba: omb --port %d", cfg.Port, err, cfg.Port+1)
	} else {
		_ = ln.Close()
	}

	url := srv.URL()
	fmt.Printf("\n  🐭 OpenMausBot (Go %s) — %s\n", version, url)
	fmt.Printf("  data: %s\n", config.DataDir())
	if autoToken {
		fmt.Printf("  auth: %s\n", srv.LoginURL())
	} else {
		fmt.Printf("  auth: aktif (memakai --token / $OMB_TOKEN)\n")
	}
	fmt.Printf("  RAM budget: 192 MB heap cap, %d workers\n", runtime.NumCPU())
	fmt.Printf("  Ctrl-C untuk berhenti\n\n")

	if *open {
		go openBrowser(srv.LoginURL())
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Listen() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	case <-ctx.Done():
		fmt.Println("\n  bye 👋")
		mgr.CancelAll()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shutCtx)
		cancel()
	}
}

func usage() {
	fmt.Print(`OpenMausBot (Go) — harness agent lokal

  omb                         jalankan server + UI (default 127.0.0.1:8799)
  omb --port 9000 --bind 0.0.0.0
  omb --token SECRET          set token akses API (atau env OMB_TOKEN; default acak)
  omb --open=false            headless (server/VPS)
  omb --data DIR              pindahkan folder data

  omb doctor                  cek semuanya: data dir, port, CLI, key, endpoint lokal
  omb check                   seperti doctor, tapi mengirim 1 permintaan sungguhan ke tiap provider
                              dan otomatis memperbaiki model yang sudah dipensiunkan
  omb keys                    daftar provider + status key
  omb use <provider> [model]  arahkan semua bot ke provider yang masih hidup (bukan key-nya)
  omb key <id> -              isi API key dari stdin (aman, tanpa history)
  omb key <id> sk-...         isi API key langsung
  omb provider openai URL MODEL [nama]
                              tambah endpoint OpenAI-compatible

Contoh:
  echo "$OPENAI_API_KEY" | omb key openai -
  omb provider openai http://127.0.0.1:11434/v1 llama3.2 "Ollama lokal"

Env var yang dibaca otomatis kalau key diset sebagai $NAMA:
  OPENAI_API_KEY, ANTHROPIC_API_KEY, OPENROUTER_API_KEY, GROQ_API_KEY, DEEPSEEK_API_KEY
`)
}

// openBrowser is best-effort: no browser on a headless box is not an error.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Start()
}
