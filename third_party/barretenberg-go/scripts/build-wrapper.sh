#!/usr/bin/env bash
#
# build-wrapper.sh — Build libbarretenberg.a for the specified platform
#
# This script downloads Aztec's pre-built libbb-external.a, compiles our
# C++ wrapper shim, and merges them into a single static archive.
#
# Version and checksums are read from checksums.json in the repo root.
#
# Usage:
#   ./scripts/build-wrapper.sh --platform linux_amd64|linux_arm64|linux_arm64_musl|darwin_amd64|darwin_arm64
#
# Supported platforms: linux_amd64, linux_arm64, linux_arm64_musl, darwin_amd64, darwin_arm64
#
# Prerequisites:
#   - clang++ with C++20 support (honours $CXX; defaults to clang++)
#   - python3 (for reading checksums.json)
#   - curl
#   - ar or llvm-ar (honours $AR; defaults to ar)
#   - git (for sparse-checkout of headers)

set -euo pipefail

# Resolve paths relative to this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INCLUDE_DIR="$REPO_ROOT/include"

# Read version info from checksums.json
BB_AZTEC_TAG="$(python3 -c "import json; print(json.load(open('$REPO_ROOT/checksums.json'))['aztec_tag'])")"
BB_AZTEC_REPO="$(python3 -c "import json; print(json.load(open('$REPO_ROOT/checksums.json'))['aztec_repo'])")"
MSGPACK_COMMIT="$(python3 -c "import json; print(json.load(open('$REPO_ROOT/checksums.json'))['msgpack_commit'])")"

PLATFORM=""

usage() {
    echo "Usage: $0 --platform linux_amd64|linux_arm64|linux_arm64_musl|darwin_amd64|darwin_arm64" >&2
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --platform)
            PLATFORM="$2"
            shift 2
            ;;
        *)
            usage
            ;;
    esac
done

if [[ -z "$PLATFORM" ]]; then
    usage
fi

# Read expected SHA256 from checksums.json
EXPECTED_SHA256="$(python3 -c "import json; print(json.load(open('$REPO_ROOT/checksums.json'))['assets']['$PLATFORM']['sha256'])")"

case "$PLATFORM" in
    linux_amd64)
        AZTEC_ARCH="amd64"
        AZTEC_OS="linux"
        LINUX_CROSS_TARGET="--target=x86_64-linux-gnu"
        LINUX_STDLIB="-stdlib=libc++"   # match Aztec's amd64 build (libc++)
        ;;
    linux_arm64)
        AZTEC_ARCH="arm64"
        AZTEC_OS="linux"
        LINUX_CROSS_TARGET="--target=aarch64-linux-gnu"
        LINUX_STDLIB="-stdlib=libc++"   # match Zig/libc++ used for arm64
        ;;
    linux_arm64_musl)
        AZTEC_ARCH="arm64"
        AZTEC_OS="linux"
        LINUX_CROSS_TARGET="-target aarch64-linux-musl"
        LINUX_STDLIB="-stdlib=libc++"
        ;;
    darwin_amd64)
        AZTEC_ARCH="amd64"
        AZTEC_OS="darwin"
        DARWIN_TARGET="-target x86_64-apple-macos11.0"
        ;;
    darwin_arm64)
        AZTEC_ARCH="arm64"
        AZTEC_OS="darwin"
        DARWIN_TARGET="-mmacosx-version-min=11.0"
        ;;
    *)
        echo "ERROR: unsupported platform '$PLATFORM'." >&2
        echo "  Supported platforms: linux_amd64, linux_arm64, linux_arm64_musl, darwin_amd64, darwin_arm64" >&2
        exit 1
        ;;
esac

if [[ "$PLATFORM" == "linux_arm64_musl" ]]; then
    if [[ "${CC:-}" != *zig-cc || "${CXX:-}" != *zig-c++ || "${AR:-}" != *zig-ar ]]; then
        echo "ERROR: linux_arm64_musl requires the pinned Zig toolchain wrappers." >&2
        echo "  Run 'make build-linux_arm64_musl', or set CC=zig-cc CXX=zig-c++ AR=zig-ar" >&2
        echo "  after installing the toolchain with scripts/install-zig.sh." >&2
        exit 1
    fi
fi

LIB_DIR="$REPO_ROOT/lib/$PLATFORM"

TARBALL_URL="${BB_AZTEC_REPO}/releases/download/${BB_AZTEC_TAG}/barretenberg-static-${AZTEC_ARCH}-${AZTEC_OS}.tar.gz"

echo "═══════════════════════════════════════════════════════════════"
echo "  Building libbarretenberg.a"
echo "  Platform:    $PLATFORM"
echo "  Aztec tag:   $BB_AZTEC_TAG"
echo "  Output:      $LIB_DIR/libbarretenberg.a"
echo "═══════════════════════════════════════════════════════════════"

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

# ── Step 1: Download and verify libbb-external.a ─────────────────────────────
echo ""
echo "▶ Step 1: Downloading libbb-external.a from Aztec release..."
echo "  URL: $TARBALL_URL"
TARBALL="$WORK_DIR/barretenberg-static.tar.gz"
curl -fsSL -o "$TARBALL" "$TARBALL_URL"

echo "  Verifying SHA256 checksum..."
if command -v sha256sum &>/dev/null; then
    ACTUAL_SHA256="$(sha256sum "$TARBALL" | awk '{print $1}')"
else
    ACTUAL_SHA256="$(shasum -a 256 "$TARBALL" | awk '{print $1}')"
fi
if [[ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]]; then
    echo "ERROR: SHA256 mismatch for barretenberg-static-${AZTEC_ARCH}-${AZTEC_OS}.tar.gz" >&2
    echo "  Expected: $EXPECTED_SHA256" >&2
    echo "  Got:      $ACTUAL_SHA256" >&2
    echo "  Refusing to continue — the tarball may have been tampered with." >&2
    exit 1
fi
echo "  Checksum OK: $ACTUAL_SHA256"
tar -xz -C "$WORK_DIR" -f "$TARBALL"

BB_EXTERNAL_A="$WORK_DIR/libbb-external.a"
if [[ ! -f "$BB_EXTERNAL_A" ]]; then
    echo "ERROR: libbb-external.a not found after extracting tarball." >&2
    echo "  Contents of work dir:" >&2
    ls -la "$WORK_DIR" >&2
    exit 1
fi
echo "  Downloaded: $(du -sh "$BB_EXTERNAL_A" | cut -f1) libbb-external.a"

# ── Step 2: Sparse-checkout barretenberg headers from aztec-packages ─────────
echo ""
echo "▶ Step 2: Fetching barretenberg headers (sparse checkout)..."
HEADERS_DIR="$WORK_DIR/az-src"
git clone \
    --filter=blob:none \
    --sparse \
    --depth=1 \
    --branch="$BB_AZTEC_TAG" \
    "$BB_AZTEC_REPO.git" \
    "$HEADERS_DIR"
git -C "$HEADERS_DIR" sparse-checkout set barretenberg/cpp/src
echo "  Headers at: $HEADERS_DIR/barretenberg/cpp/src"

# ── Step 2b: Create stubs for external headers not in the barretenberg source tree ──
echo ""
echo "▶ Step 2b: Creating external-dependency stubs..."
STUBS_DIR="$WORK_DIR/stubs"
mkdir -p "$STUBS_DIR/tracy"
cat > "$STUBS_DIR/tracy/Tracy.hpp" << 'TRACY_EOF'
#pragma once
// Stub Tracy.hpp — all macros are no-ops when TRACY_ENABLE is not defined.
#ifndef TRACY_ENABLE
#  define TracyAlloc(ptr, size)
#  define TracyFree(ptr)
#  define TracyAllocS(ptr, size, depth)
#  define TracyFreeS(ptr, depth)
#  define TracyAllocN(ptr, size, name)
#  define TracyFreeN(ptr, name)
#  define TracyAllocNS(ptr, size, depth, name)
#  define TracyFreeNS(ptr, depth, name)
#  define TracySecureAlloc(ptr, size)
#  define TracySecureFree(ptr)
#  define TracySecureAllocS(ptr, size, depth)
#  define TracySecureFreeS(ptr, depth)
#  define ZoneScoped
#  define ZoneScopedN(x)
#  define ZoneScopedC(x)
#  define ZoneScopedNC(x, y)
#  define ZoneNamedN(x, name, active)
#  define FrameMark
#  define FrameMarkNamed(x)
#  define FrameMarkStart(x)
#  define FrameMarkEnd(x)
#endif
TRACY_EOF
echo "  Created stubs/tracy/Tracy.hpp"

# ── Step 2c: Download msgpack-c include headers (barretenberg external dependency) ──
echo ""
echo "▶ Step 2c: Downloading msgpack-c include headers..."
MSGPACK_DIR="$WORK_DIR/msgpack-c"
mkdir -p "$MSGPACK_DIR"
curl -fsSL "https://github.com/AztecProtocol/msgpack-c/archive/${MSGPACK_COMMIT}.tar.gz" \
    | tar -xz -C "$MSGPACK_DIR" --strip-components=1
echo "  msgpack-c include at: $MSGPACK_DIR/include"

# ── Step 3: Compile barretenberg_wrapper.cpp ─────────────────────────────────
echo ""
echo "▶ Step 3: Compiling barretenberg_wrapper.cpp..."
WRAPPER_O="$WORK_DIR/barretenberg_wrapper.o"

# On macOS, detect the SDK path and use -isysroot so clang can find both
# C standard headers (stddef.h, stdlib.h, etc.) and C++ headers when
# cross-compiling (e.g., x86_64 on an arm64 runner).
SDK_FLAGS=()
if [[ "$AZTEC_OS" == "darwin" ]]; then
    SDK_PATH="$(xcrun --show-sdk-path 2>/dev/null || true)"
    if [[ -n "$SDK_PATH" ]]; then
        SDK_FLAGS=(-isysroot "$SDK_PATH")
        echo "  Using SDK: $SDK_PATH"
    fi
fi

CLANG_FLAGS=(
    -std=c++20
    -fPIC
    -O2
    -fvisibility=hidden
    -fvisibility-inlines-hidden
    "${SDK_FLAGS[@]}"
    -I "$MSGPACK_DIR/include"
    -I "${STUBS_DIR}"
    -I "$HEADERS_DIR/barretenberg/cpp/src"
    -I "$INCLUDE_DIR"
    -DBB_VERSION="\"$BB_AZTEC_TAG\""
    -c "$REPO_ROOT/wrapper/barretenberg_wrapper.cpp"
    -o "$WRAPPER_O"
)

# Apply Darwin-specific target flags
if [[ "$AZTEC_OS" == "darwin" && -n "${DARWIN_TARGET:-}" ]]; then
    CLANG_FLAGS=($DARWIN_TARGET "${CLANG_FLAGS[@]}")
fi

# Apply Linux target and C++ standard-library flags. The musl build uses the
# pinned Zig wrapper installed by scripts/install-zig.sh.
if [[ "$AZTEC_OS" == "linux" && ( "${CXX:-clang++}" == *clang* || "${CXX:-clang++}" == *zig-c++ ) ]]; then
    STDLIB_FLAGS=()
    [[ -n "${LINUX_STDLIB:-}" ]] && STDLIB_FLAGS=($LINUX_STDLIB)
    CLANG_FLAGS=($LINUX_CROSS_TARGET "${STDLIB_FLAGS[@]}" "${CLANG_FLAGS[@]}")
fi

${CXX:-clang++} "${CLANG_FLAGS[@]}"
echo "  Compiled: $WRAPPER_O"

EXTRA_OBJECTS=()
if [[ "$PLATFORM" == "linux_arm64_musl" ]]; then
    MUSL_COMPAT_O="$WORK_DIR/musl_compat.o"
    ${CC:-clang} -target aarch64-linux-musl -fPIC -O2 \
        -c "$REPO_ROOT/wrapper/musl_compat.c" -o "$MUSL_COMPAT_O"
    EXTRA_OBJECTS+=("$MUSL_COMPAT_O")
    echo "  Compiled: $MUSL_COMPAT_O"
fi

# ── Step 4: Merge wrapper.o + libbb-external.a → libbarretenberg.a ───────────
echo ""
echo "▶ Step 4: Merging into libbarretenberg.a..."
mkdir -p "$LIB_DIR"
OUTPUT_A="$LIB_DIR/libbarretenberg.a"
OUTPUT_CANDIDATE="$LIB_DIR/.libbarretenberg.a.$$.tmp"
trap 'rm -rf "$WORK_DIR"; rm -f "$OUTPUT_CANDIDATE"' EXIT

# Append approach: copy the Aztec pre-built archive as-is, then add our wrapper
# object on top. ar rcs appends to an existing archive without touching existing
# members. llvm-ar handles both ELF and Mach-O archives.
cp "$BB_EXTERNAL_A" "$OUTPUT_CANDIDATE"
${AR:-ar} rcs "$OUTPUT_CANDIDATE" "$WRAPPER_O" ${EXTRA_OBJECTS[@]:+"${EXTRA_OBJECTS[@]}"}

# Strip debug symbols to reduce archive size (~544MB → ~48MB on darwin).
# Uses strip -S (macOS) or objcopy --strip-debug via strip (Linux).
# This is critical for staying under GitHub's 100MB file size limit.
echo "  Stripping debug symbols..."
if [[ "$AZTEC_OS" == "darwin" ]]; then
    strip -S "$OUTPUT_CANDIDATE" 2>/dev/null || true
else
    ${STRIP:-strip} --strip-debug "$OUTPUT_CANDIDATE" 2>/dev/null || true
fi

${AR:-ar} t "$OUTPUT_CANDIDATE" >/dev/null
mv -f "$OUTPUT_CANDIDATE" "$OUTPUT_A"
echo "  Output: $(du -sh "$OUTPUT_A" | cut -f1) $OUTPUT_A"

echo ""
echo "Done! libbarretenberg.a built for $PLATFORM (Aztec $BB_AZTEC_TAG)"
echo ""
echo "Next steps:"
echo "  go test -v ./..."
