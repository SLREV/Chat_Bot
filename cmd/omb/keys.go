package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// keysCommand handles `omb keys` and `omb key <id> <value>` so a headless box
// never needs the browser to store a credential.
//
// Keys are passed as an argument (visible in shell history) or, better, read
// from stdin with `-` so they never land in history or in `ps` output.
func keysCommand(args []string) int {
	provs, err := config.LoadProviders()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gagal membaca provider:", err)
		return 1
	}

	switch args[0] {
	case "keys", "providers":
		if len(provs.List()) == 0 {
			fmt.Println("belum ada provider")
			return 0
		}
		fmt.Printf("%-14s %-26s %-10s %-28s %s\n", "ID", "NAMA", "KIND", "MODEL", "KEY")
		for _, m := range provs.Masked() {
			key := "-"
			switch {
			case m.KeyFromEnv:
				key = m.KeyHint + " (env)"
			case m.HasKey:
				key = m.KeyHint
			case m.KeyHint == "":
				key = "(tanpa key)"
			}
			fmt.Printf("%-14s %-26s %-10s %-28s %s\n", m.ID, m.Name, m.Kind, m.DefaultModel, key)
		}
		return 0

	case "use":
		// omb use <provider-id> [model] — set the provider for every bot that
		// currently points at a dead one, without opening the browser.
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "pakai: omb use <provider-id> [model]")
			return 2
		}
		id := args[1]
		pr, ok := provs.ByID(id)
		if !ok {
			fmt.Fprintf(os.Stderr, "provider %q tidak ada. Lihat: omb keys\n", id)
			return 1
		}
		model := pr.DefaultModel
		if len(args) > 2 {
			model = args[2]
		} else if len(pr.Models) > 0 {
			model = pr.Models[0]
		}
		if strings.TrimSpace(model) == "" {
			fmt.Fprintf(os.Stderr, "%s belum punya model — jalankan `omb check --fix` atau isi daftar model dulu\n", pr.Name)
			return 1
		}
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "gagal membaca setelan:", err)
			return 1
		}
		n := 0
		for _, b := range cfg.Snapshot() {
			b.Engine = pr.ID
			b.Model = model
			cfg.UpsertBot(b)
			n++
		}
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "gagal menyimpan:", err)
			return 1
		}
		fmt.Printf("%d bot memakai %s / %s\n", n, pr.Name, model)
		return 0

	case "key":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "pakai: omb key <provider-id> [value|-]")
			return 2
		}
		id := args[1]
		pr, ok := provs.ByID(id)
		if !ok {
			// Allow a unique prefix, so "openai" can address "openai-<suffix>".
			for _, p := range provs.List() {
				if strings.HasPrefix(p.ID, id) {
					pr, ok = p, true
					break
				}
			}
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "provider %q tidak ada. Lihat: omb keys\n", id)
			return 1
		}
		if len(args) < 3 {
			fmt.Printf("%s: %s\n", pr.Name, config.Mask(pr).KeyHint)
			return 0
		}
		value := strings.TrimSpace(args[2])
		if value == "-" {
			raw, err := readAllStdin()
			if err != nil {
				fmt.Fprintln(os.Stderr, "gagal membaca stdin:", err)
				return 1
			}
			value = strings.TrimSpace(raw)
		}
		clean, notes := config.SanitizeKey(value)
		if clean == "" {
			fmt.Fprintln(os.Stderr, "key kosong; tidak ada yang disimpan")
			return 1
		}
		if len(notes) > 0 {
			fmt.Println("· dibersihkan:", strings.Join(notes, ", "))
		}
		if warn := config.KeyWarning(pr, clean); warn != "" {
			fmt.Fprintln(os.Stderr, "! "+warn)
		}
		value = clean
		pr.APIKey = value
		if err := provs.Upsert(pr); err != nil {
			fmt.Fprintln(os.Stderr, "gagal menyimpan:", err)
			return 1
		}
		fmt.Printf("✓ key untuk %s disimpan di %s (0600)\n", pr.Name, config.ProvidersPath())
		return 0

	case "provider": // add a custom endpoint from a script
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "pakai: omb provider <kind> <base-url> <default-model> [name]")
			return 2
		}
		kind := args[1]
		if kind != "openai" && kind != "anthropic" {
			fmt.Fprintln(os.Stderr, "kind harus openai atau anthropic")
			return 2
		}
		name := "Custom"
		if len(args) > 4 {
			name = strings.Join(args[4:], " ")
		}
		pr := config.Provider{
			ID:           config.NewProviderID(),
			Name:         name,
			Kind:         kind,
			BaseURL:      strings.TrimRight(args[2], "/"),
			DefaultModel: args[3],
			Enabled:      true,
		}
		if err := provs.Upsert(pr); err != nil {
			fmt.Fprintln(os.Stderr, "gagal menyimpan:", err)
			return 1
		}
		fmt.Printf("✓ provider %s (%s) ditambahkan, id=%s\n", name, pr.BaseURL, pr.ID)
		return 0
	}
	return 2
}

func readAllStdin() (string, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return b.String(), nil
		}
	}
	return b.String(), nil
}
