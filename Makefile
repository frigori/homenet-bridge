VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# No local Go? `make GO="docker run --rm -v $$PWD:/src -w /src golang:1.25-alpine go" test`
GO ?= go

.PHONY: build test vet dist app aar clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/homenet-bridge ./cmd/homenet-bridge

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

dist:
	for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" \
			-o dist/homenet-bridge-$$os-$$arch$$ext ./cmd/homenet-bridge || exit 1; \
	done

# The macOS menu bar app, with the CLI inside. Needs the Xcode command line tools (swift).
# No local Go? Build bin/homenet-bridge-darwin first, e.g. with Docker:
#   docker run --rm -v $$PWD:/src -w /src -e GOOS=darwin -e GOARCH=arm64 -e CGO_ENABLED=0 \
#     golang:1.25-alpine go build -ldflags "$(LDFLAGS)" -o bin/homenet-bridge-darwin ./cmd/homenet-bridge
# and run `make app CLI=bin/homenet-bridge-darwin`.
CLI ?=
app:
	@if [ -z "$(CLI)" ]; then \
		CGO_ENABLED=0 GOOS=darwin $(GO) build -ldflags "$(LDFLAGS)" -o bin/homenet-bridge-darwin ./cmd/homenet-bridge; \
	fi
	macos/build-app.sh $(or $(CLI),bin/homenet-bridge-darwin) $(VERSION:v%=%) dist

# The Android library (mobile/), for the Android app. Needs the Android SDK and NDK
# (ANDROID_HOME, ANDROID_NDK_HOME) and a JDK.
# gomobile and gobind are pinned in go.mod; gomobile looks for gobind on the PATH.
aar:
	GOBIN=$(CURDIR)/build/tools $(GO) install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
	PATH="$(CURDIR)/build/tools:$$PATH" gomobile init
	PATH="$(CURDIR)/build/tools:$$PATH" gomobile bind -target=android/arm64,android/amd64 -androidapi 24 \
		-javapkg io.github.frigori.homenetbridge -ldflags "$(LDFLAGS)" -o build/homenet-bridge.aar ./mobile

clean:
	rm -rf bin dist build macos/.build
