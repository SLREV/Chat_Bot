APP      := omb
VERSION  ?= 0.6.6
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := CGO_ENABLED=0

.PHONY: all build run test vet fmt clean dist size

all: build

## build: binary for this machine, stripped, static, UI embedded
build:
	$(GOFLAGS) go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/omb
	@ls -lh bin/$(APP)

## run: build-free run on http://127.0.0.1:8799
run:
	go run ./cmd/omb

## test: unit + API tests (no network needed)
test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

## dist: cross-compiled binaries (all under 10 MB, no runtime deps)
dist:
	mkdir -p dist
	$(GOFLAGS) GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-amd64      ./cmd/omb
	$(GOFLAGS) GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-linux-arm64      ./cmd/omb
	$(GOFLAGS) GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-macos-arm64      ./cmd/omb
	$(GOFLAGS) GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-macos-amd64      ./cmd/omb
	$(GOFLAGS) GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(APP)-windows-amd64.exe ./cmd/omb
	@ls -lh dist

## size: where the bytes went — useful when trimming for a small machine
size: build
	@echo "binary: $$(du -h bin/$(APP) | cut -f1)"
	$(GOFLAGS) go tool nm -size bin/$(APP) 2>/dev/null | sort -k2 -n -r | head -n 15 || true

## win: folder distribusi Windows siap klik-dua-kali
win: dist
	rm -rf dist/openmausbot-windows-x64
	mkdir -p dist/openmausbot-windows-x64
	cp dist/omb-windows-amd64.exe dist/openmausbot-windows-x64/omb.exe
	cp scripts/windows/omb.cmd dist/openmausbot-windows-x64/
	cp scripts/windows/README-WINDOWS.txt dist/openmausbot-windows-x64/
	cd dist && zip -r -q openmausbot-windows-x64.zip openmausbot-windows-x64
	@ls -lh dist/openmausbot-windows-x64.zip
	@echo "Isi: omb.exe + omb.cmd + README-WINDOWS.txt (klik dua kali omb.exe)"

clean:
	rm -rf bin dist
