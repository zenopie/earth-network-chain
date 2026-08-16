.PHONY: build build-all build-linux_arm64_musl test clean bench

PLATFORM := $(shell go env GOOS)_$(shell go env GOARCH)
ZIG_DIR ?= $(or $(XDG_CACHE_HOME),/tmp)/barretenberg-go/zig-0.14.1-$(shell uname -m)

# Build libbarretenberg.a for the current platform
build:
	./scripts/build-wrapper.sh --platform $(PLATFORM)

# Build for a specific platform (e.g., make build-linux_amd64)
build-%:
	./scripts/build-wrapper.sh --platform $*

# Build the musl variant with the pinned Zig toolchain.
build-linux_arm64_musl:
	@ZIG_BIN="$$(./scripts/install-zig.sh "$(ZIG_DIR)")"; \
	PATH="$$ZIG_BIN:$$PATH" CC=zig-cc CXX=zig-c++ AR=zig-ar \
		./scripts/build-wrapper.sh --platform linux_arm64_musl

# Build for all platforms (requires cross-compilation toolchains)
build-all: build-linux_amd64 build-linux_arm64 build-linux_arm64_musl build-darwin_amd64 build-darwin_arm64

# Run tests (requires lib/<platform>/libbarretenberg.a to exist)
test:
	go test -v -count=1 ./...

# Run benchmarks
bench:
	go test -bench=. -benchmem -run=^$$ ./...

# Clean temporary build artifacts (does not remove committed lib/*.a files)
clean:
	rm -rf /tmp/bb-build-*
