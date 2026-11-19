BIN        := flowstate
BUILD_DIR  := .
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -ldflags "-X main.version=$(VERSION)"

# --- Linux desktop UI toolchain ---------------------------------------------
# The `flowstate ui` window links Fyne/GLFW, which needs Wayland + Xxf86vm +
# libffi dev files. Distributions ship them as packages (see README). For
# hosts without root, `make ui-deps` unpacks them into .deps/ and these
# exports point the C toolchain at that prefix.
UI_DEPS     := .deps/usr
UI_DEPS_INC := $(wildcard $(UI_DEPS)/include)
UI_DEPS_LIB := $(firstword $(wildcard $(UI_DEPS)/lib/*-linux-gnu))
ifneq ($(UI_DEPS_INC),)
UI_PC_PATH := $(abspath $(UI_DEPS_LIB)/pkgconfig)
# Only prepend when there is an existing value: a trailing ':' would make
# pkg-config also search the current directory for .pc files.
ifneq ($(PKG_CONFIG_PATH),)
UI_PC_PATH := $(UI_PC_PATH):$(PKG_CONFIG_PATH)
endif
export PKG_CONFIG_PATH := $(UI_PC_PATH)
export CGO_CFLAGS := -I$(abspath $(UI_DEPS_INC)) $(foreach d,$(wildcard $(UI_DEPS)/include/*-linux-gnu),-I$(abspath $(d))) $(CGO_CFLAGS)
export CGO_LDFLAGS := -L$(abspath $(UI_DEPS_LIB)) $(CGO_LDFLAGS)
endif

.PHONY: build ui-deps test test-verbose test-race test-e2e fmt vet lint cover install clean snapshot tidy

# Missing UI dev files produce a wall of C errors; catch it up front with the
# exact fix instead. Linux only (macOS/Windows use their own window backends),
# and skipped when the headers are installed system-wide.
check-ui-deps:
	@if [ "$$(uname -s)" = Linux ] && [ ! -f /usr/include/wayland-client-core.h ] && [ ! -d .deps/usr/include ]; then \
		echo "The desktop UI needs Wayland/GLFW dev files that aren't installed:"; \
		echo "  sudo apt-get install -y libwayland-dev libffi-dev libxxf86vm-dev libgl1-mesa-dev libxcursor-dev libxi-dev"; \
		echo "  no root?  make ui-deps   # unpacks them into .deps/ instead"; \
		exit 1; \
	fi

build: check-ui-deps
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BIN) ./cmd/flowstate

ui-deps:
	@./scripts/ui-deps.sh

install:
	go install $(LDFLAGS) ./cmd/flowstate

test:
	go test ./... -timeout 120s

test-race:
	go test -race ./... -timeout 120s

test-verbose:
	go test ./... -v -timeout 120s

test-e2e: build
	go test ./tests/e2e/... -v

tidy:
	go mod tidy

fmt:
	gofmt -w ./...

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		go vet ./...; \
	fi

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -f $(BIN) coverage.out
	rm -rf dist/

.DEFAULT_GOAL := build
