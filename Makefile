# rm2-ai-daemon — build, test, and deploy to the reMarkable 2.
#
# The device is armv7l running a 5.4 kernel. A static Go binary is immune to
# the firmware's glibc and needs nothing installed on device.

BINARY  := rm2-ai-daemon
PKG     := ./cmd/rm2-ai-daemon
BUILD   := build

# The rM2 over USB. Override for wifi: make deploy DEVICE=root@192.168.1.50
DEVICE  ?= root@10.11.99.1
SSH_OPTS ?= -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null

REMOTE_BIN  := /home/root/$(BINARY)
REMOTE_CONF := /home/root/.config/rm2-ai

GOARCH_ENV := GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build build-host test test-update lint vet fmt check deploy install uninstall clean

all: check build

## build: cross-compile for the device
build:
	@mkdir -p $(BUILD)
	$(GOARCH_ENV) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD)/$(BINARY) $(PKG)
	@ls -lh $(BUILD)/$(BINARY) | awk '{print "built " $$9 " (" $$5 ")"}'

## build-host: compile for this machine (dry-run commands only; device I/O is linux-only)
build-host:
	@mkdir -p $(BUILD)
	go build -o $(BUILD)/$(BINARY)-host $(PKG)

## test: run the unit tests (all off-device)
test:
	go test ./...

## test-update: refresh the golden files
test-update:
	go test ./internal/layout -update

## vet: report suspicious constructs
vet:
	go vet ./...

## fmt: check formatting
fmt:
	@out=$$(gofmt -l . 2>/dev/null | grep -v '^m0/' || true); \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

## lint: run golangci-lint when it is installed
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; \
	else echo "golangci-lint not installed; running go vet instead"; go vet ./...; fi

## check: everything CI runs
check: fmt vet test

## deploy: copy the binary to the device
deploy: build
	scp $(SSH_OPTS) $(BUILD)/$(BINARY) $(DEVICE):$(REMOTE_BIN)
	@echo "deployed to $(DEVICE):$(REMOTE_BIN)"

## install: deploy the binary, seed the config, install and start the daemon
##   Firmware updates wipe the unit; rerun `make install` afterwards.
install: deploy
	ssh $(SSH_OPTS) $(DEVICE) 'mkdir -p $(REMOTE_CONF)'
	@if ssh $(SSH_OPTS) $(DEVICE) 'test -f $(REMOTE_CONF)/config.toml'; then \
		echo "keeping the existing $(REMOTE_CONF)/config.toml"; \
	else \
		scp $(SSH_OPTS) deploy/config.example.toml $(DEVICE):$(REMOTE_CONF)/config.toml; \
		echo "seeded $(REMOTE_CONF)/config.toml — add your API key"; \
	fi
	scp $(SSH_OPTS) deploy/rm2-ai.service $(DEVICE):/etc/systemd/system/rm2-ai.service
	ssh $(SSH_OPTS) $(DEVICE) 'systemctl daemon-reload && systemctl enable rm2-ai && systemctl restart rm2-ai'
	@echo "daemon running. logs: ssh $(DEVICE) journalctl -u rm2-ai -f"

## uninstall: stop the daemon, remove unit and binary (the config is left alone)
uninstall:
	-ssh $(SSH_OPTS) $(DEVICE) 'systemctl disable --now rm2-ai 2>/dev/null; rm -f /etc/systemd/system/rm2-ai.service; systemctl daemon-reload'
	ssh $(SSH_OPTS) $(DEVICE) 'rm -f $(REMOTE_BIN)'

## clean: remove build output
clean:
	rm -rf $(BUILD)
