# build-tags.txt lists the SQLite features and the tailscale.com features the tunnel omits.
TAGS := $(shell cat build-tags.txt)
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/pocketcontext-labs/pocketcontext-template/internal/bundle.Version=$(VERSION)
GOBUILD := go build -trimpath -tags $(TAGS) -ldflags "$(LDFLAGS)"

.PHONY: ui dev build windows test names identity e2e
ui:
	cd ui && pnpm install --frozen-lockfile && pnpm build
dev:
	node scripts/dev.mjs --db "$(DB)"
build:
	CGO_ENABLED=1 $(GOBUILD) -o bin/templatecontext ./cmd/templatecontext
# Cross-compile from Linux or macOS with Zig as the C compiler (ZIG=/path/to/zig).
windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 ZIG_TARGET=x86_64-windows-gnu CC="$(CURDIR)/scripts/zig-cc" \
		$(GOBUILD) -o bin/windows_amd64/templatecontext.exe ./cmd/templatecontext
test:
	CGO_ENABLED=1 go test -tags $(TAGS) ./...
	node --test tests/dev_launcher.test.mjs
	python3 -B -m unittest discover -s tests -p test_release.py
# The repository and the binary must not contain the retired product name.
names: build
	python3 tests/name_check.py --binary bin/templatecontext
# A derived product must not keep the template identity (inverted in the template itself).
identity: build
	python3 tests/identity_check.py --binary bin/templatecontext
e2e: names identity
	python3 tests/e2e.py --binary bin/templatecontext
