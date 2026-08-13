GO ?= go
GORELEASER ?= goreleaser
MODULE := github.com/tidbcloud/ti-cli
BINARY_NAME := ti
BIN_DIR := bin
TI_BIN := $(BIN_DIR)/$(BINARY_NAME)
TELEMETRY_BACKEND_BIN := $(BIN_DIR)/ti-telemetry-backend
TELEMETRY_MIGRATOR_BIN := $(BIN_DIR)/ti-telemetry-migrate
TELEMETRY_E2E_ENV := e2e/.env.telemetry
LIVE_E2E_PROFILE ?= live-e2e
LIVE_E2E_RUN = TI_E2E_BIN="$(abspath $(TI_BIN))" TI_LIVE=1 TI_PROFILE="$(LIVE_E2E_PROFILE)" $(GO) test ./e2e -count=1 -v -timeout 30m

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.version=$(VERSION) \
	-X $(MODULE)/internal/version.commit=$(COMMIT) \
	-X $(MODULE)/internal/version.date=$(DATE) \
	-X $(MODULE)/internal/version.installSource=local \
	-X $(MODULE)/internal/version.releaseChannel=stable

ifneq ($(strip $(TELEMETRY_ENDPOINT)),)
LDFLAGS += -X $(MODULE)/internal/version.telemetryEndpoint=$(TELEMETRY_ENDPOINT)
endif

.PHONY: all build build-telemetry-backend build-telemetry-migrator test e2e telemetry-e2e live-e2e live-e2e-configure live-e2e-db live-e2e-fs live-e2e-fs-git live-e2e-fs-journal live-e2e-fs-vault release-snapshot clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(TI_BIN) ./cmd/ti

build-telemetry-backend:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(TELEMETRY_BACKEND_BIN) ./cmd/ti-telemetry-backend

build-telemetry-migrator:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(TELEMETRY_MIGRATOR_BIN) ./cmd/ti-telemetry-migrate

test:
	$(GO) test ./...

e2e: build
	TI_E2E_BIN="$(abspath $(TI_BIN))" $(GO) test ./e2e -count=1 -v

telemetry-e2e: build build-telemetry-backend build-telemetry-migrator
	@test -f "$(TELEMETRY_E2E_ENV)" || { echo "missing $(TELEMETRY_E2E_ENV); set TI_TEST_TELEMETRY_TIDB_DSN in that ignored file" >&2; exit 2; }
	@set -a; . "$(TELEMETRY_E2E_ENV)"; set +a; \
		TI_E2E_BIN="$(abspath $(TI_BIN))" TI_TELEMETRY_BACKEND_E2E_BIN="$(abspath $(TELEMETRY_BACKEND_BIN))" TI_TELEMETRY_MIGRATOR_E2E_BIN="$(abspath $(TELEMETRY_MIGRATOR_BIN))" TI_TELEMETRY_E2E=1 $(GO) test ./e2e -count=1 -v -run '^TestTelemetryDeliveryToTiDB$$'

live-e2e: build
	$(LIVE_E2E_RUN) -run '^TestLive'

live-e2e-configure: build
	$(LIVE_E2E_RUN) -run '^TestLive(ProfileConfigured|CLICommandSurface)$$'

live-e2e-db: build
	$(LIVE_E2E_RUN) -run '^TestLiveDB'

live-e2e-fs: build
	$(LIVE_E2E_RUN) -run '^TestLive(FSRemoteInventoryLifecycle|FSCommandSurface|FSFileSystemTokenLifecycle|FSConfigurationFreeAccess|FSDataPlaneLifecycle|FSMountRuntime|FSWebDAVMountRuntime)$$'

live-e2e-fs-git: build
	$(LIVE_E2E_RUN) -run '^TestLiveFSGit'

live-e2e-fs-journal: build
	$(LIVE_E2E_RUN) -run '^TestLiveFSJournal'

live-e2e-fs-vault: build
	$(LIVE_E2E_RUN) -run '^TestLiveFSVault'

release-snapshot:
	$(GORELEASER) release --snapshot --clean

clean:
	rm -rf $(BIN_DIR) dist
