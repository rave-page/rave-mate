# rave-mate - see CLAUDE.md for the full command table.
BIN := rave-mate
PKG := ./cmd/rave-mate
DIST := dist

# Windows: -H windowsgui drops the console window (tray app). .exe suffix.
# -extldflags=-static statically links the MinGW C/C++ runtime (libgcc/libstdc++/winpthread) so the
# exe runs on a clean machine with no MinGW DLLs beside it (matches the CI build). Vendored runtime
# DLLs (SpoutLibrary.dll, openvr_api.dll) are still shipped beside the exe - they're not MinGW.
GOOS := $(shell go env GOOS)
LDFLAGS := -s -w
ifeq ($(GOOS),windows)
  BIN := rave-mate.exe
  LDFLAGS += -H windowsgui -extldflags=-static
endif

# SPOUT=1 builds the videoshare Spout backend (Windows GPU video share). Needs the SDK in
# third_party/spout (run `make spout-sdk`); the DLL is copied next to the exe at build.
TAGS :=
ifeq ($(SPOUT),1)
  TAGS := spout
endif
# ZIG=1 links the ravezig native core (Zig DSP: sinc resampler + waveform kernels).
# Needs `make zig` first (zig >= 0.16). Untagged builds keep the pure-Go paths.
ifeq ($(ZIG),1)
  TAGS += zigdsp
endif
# ZIG=1 also links the raveui Zig webview render layer (native/zigui, tag zigui) —
# ONE switch turns on all Zig natives; `make zig` builds all libs.
ifeq ($(ZIG),1)
  TAGS += zigui zigvr
endif
# ZIGVR=1 links only the ravevr VR-overlay raster lib (Zig display-list executor,
# pixel-identical to the Go raster - see .devnotes/ZIG_VR_OVERLAY.md).
ifeq ($(ZIGVR),1)
  TAGS += zigvr
endif

.PHONY: build build-spout spout-sdk zig build-zig run service vet fmt test tidy soak vuln clean all generate-api generate-icon

all: generate-api fmt vet test build

# Regenerate the filtered API client from the live OpenAPI schema (branch→URL like the
# web repo). Run first whenever the API changes. Output: internal/apiclient/.
generate-api:
	cd tools/genapi && go run .

# Recompile internal/ui/assets/icon.png into the committed Windows resource object so the
# .exe shows the brand icon in taskbar/launcher/Explorer. Run only when the icon changes;
# the .syso is checked in and auto-linked by `go build` (local + CI cross-build).
# Output: cmd/rave-mate/rsrc_windows_amd64.syso.
generate-icon:
	cd tools/winicon && go run .

build:
	go build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(DIST)/$(BIN) $(PKG)
ifeq ($(SPOUT),1)
ifeq ($(GOOS),windows)
	cp third_party/spout/bin/SpoutLibrary.dll $(DIST)/
endif
endif

# Fetch + SHA-verify the Spout2 SDK into third_party/spout (Windows GPU video share).
# Windows dev → PowerShell; Linux/mac (incl. mingw cross-build) → the POSIX twin (same URL+SHA).
spout-sdk:
ifeq ($(GOOS),windows)
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fetch-spout.ps1
else
	bash scripts/fetch-spout.sh
endif

# Build with the Spout backend enabled (fetches the SDK first if needed).
build-spout: spout-sdk
	$(MAKE) build SPOUT=1

# Build the ravezig native core (native/zigcore → zig-out/lib/libravezig.a).
zig:
ifeq ($(GOOS),windows)
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-zig.ps1
else
	bash scripts/build-zig.sh
endif

# Build with the Zig DSP core enabled (builds the lib first).
build-zig: zig
	$(MAKE) build ZIG=1

run:
	go run $(PKG)

service:
	go run $(PKG) --service

vet:
	go vet ./...

fmt:
	gofmt -w .

test:
	go test ./...

tidy:
	go mod tidy

soak:        ## supply-chain 7-day soak gate
	bash scripts/check-release-age.sh

vuln:
	govulncheck ./...

clean:
	rm -rf $(DIST)
# Build with both Zig libs linked (DSP core + VR-overlay raster).
.PHONY: build-zig-all
build-zig-all: zig
	$(MAKE) build ZIG=1 ZIGVR=1
