BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')
APPNAME := earth

# do not override user values
ifeq (,$(VERSION))
  VERSION := $(shell git describe --exact-match 2>/dev/null)
  # if VERSION is empty, then populate it with branch name and raw commit hash
  ifeq (,$(VERSION))
    VERSION := $(BRANCH)-$(COMMIT)
  endif
endif

# Update the ldflags with the app, client & server names
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=$(APPNAME) \
	-X github.com/cosmos/cosmos-sdk/version.AppName=$(APPNAME)d \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS := -ldflags '$(ldflags)'

##############
###  Test  ###
##############

test-unit:
	@echo Running unit tests...
	@go test -mod=readonly -v -timeout 30m ./...

test-race:
	@echo Running unit tests with race condition reporting...
	@go test -mod=readonly -v -race -timeout 30m ./...

test-cover:
	@echo Running unit tests and creating coverage report...
	@go test -mod=readonly -v -timeout 30m -coverprofile=$(COVER_FILE) -covermode=atomic ./...
	@go tool cover -html=$(COVER_FILE) -o $(COVER_HTML_FILE)
	@rm $(COVER_FILE)

bench:
	@echo Running unit tests with benchmarking...
	@go test -mod=readonly -v -timeout 30m -bench=. ./...

test: govet govulncheck test-unit

.PHONY: test test-unit test-race test-cover bench

#################
###  Install  ###
#################

all: install

install:
	@echo "--> ensure dependencies have not been modified"
	@go mod verify
	@echo "--> installing $(APPNAME)d"
	@go install $(BUILD_FLAGS) -mod=readonly ./cmd/$(APPNAME)d

.PHONY: all install

##################
###  Protobuf  ###
##################

# Use this target if you do not want to use Ignite for generating proto files

proto-deps:
	@echo "Installing proto deps"
	@echo "Proto deps present, run 'go tool' to see them"

# buf directly, not `ignite generate proto-go`.
#
# Ignite only ever orchestrated buf with the template below, moved the output
# into place, and ran `go mod tidy`. Verified 2026-08-27: buf on this template
# reproduces all 37 generated files byte-for-byte.
#
# Dropping it removes a global binary from the build requirements -- ignite is
# not in go.mod, so it was the one tool nobody could pin -- and with it a trap.
# Ignite shells out to a binary named after go.mod's `go` line, literally
# `go1.25.10`, and nothing ever creates a file by that name: GOTOOLCHAIN
# downloads that toolchain into the module cache and dispatches to it internally
# rather than putting it on PATH. So `make proto-gen` failed with
#   go mod tidy: go: cannot find "go1.25.10" in PATH
# on every machine, whatever its toolchain, until someone put a shim there.
# buf's plugins are `go tool` invocations, which use the go on PATH normally.
PROTO_OUT := .proto-gen
proto-gen:
	@echo "Generating protobuf files..."
	@rm -rf $(PROTO_OUT)
	@buf generate --template proto/buf.gen.gogo.yaml --output $(PROTO_OUT)
	@# gocosmos emits into the full module path; the tree it wants is at the root.
	@cp -R $(PROTO_OUT)/github.com/earth-network/earth/. .
	@rm -rf $(PROTO_OUT)
	@go mod tidy

.PHONY: proto-gen

#################
###  Genesis  ###
#################

# networks/genesis.json is a build artifact, not a file anyone edits. It is written
# from networks/genesis/ — see networks/genesis/README.md.

genesis:
	@scripts/build-genesis.sh

# For CI: fails if the committed artifact no longer matches its sources.
genesis-check:
	@scripts/build-genesis.sh --check

.PHONY: genesis genesis-check

#################
###  Linting  ###
#################

lint:
	@echo "--> Running linter"
	@go tool github.com/golangci/golangci-lint/cmd/golangci-lint run ./... --timeout 15m

lint-fix:
	@echo "--> Running linter and fixing issues"
	@go tool github.com/golangci/golangci-lint/cmd/golangci-lint run ./... --fix --timeout 15m

.PHONY: lint lint-fix

###################
### Development ###
###################

govet:
	@echo Running go vet...
	@go vet ./...

govulncheck:
	@echo Running govulncheck...
	@go tool golang.org/x/vuln/cmd/govulncheck@latest
	@govulncheck ./...

.PHONY: govet govulncheck