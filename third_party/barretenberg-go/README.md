# barretenberg-go (vendored, pinned to bb v5.0.0)

Vendored copy of [`github.com/burnt-labs/barretenberg-go`](https://github.com/burnt-labs/barretenberg-go)
— CGo bindings to Barretenberg's UltraHonk verifier — used by
`zk/ultrahonk` to verify zkPassport (Noir/UltraHonk) proofs on-chain.

Referenced from the chain via a `replace` directive in the root `go.mod`.

## Why vendored / why pinned to v5.0.0

The upstream release ships the native lib built against Aztec **v5.0.0-rc.1**.
Barretenberg's Fiat-Shamir transcript changed between `rc.1` and the **v5.0.0**
final release, so an rc.1 verifier rejects v5.0.0 proofs (and vice-versa).
zkPassport uses **bb v5.0.0**, so `checksums.json` here pins `aztec_tag: v5.0.0`
and the native lib must be built against it. The Go source is unchanged.

Flavor: default **poseidon2** (`UltraZKFlavor`) — the natural choice for a
non-EVM chain and zkPassport's internal recursive format. No wrapper edits.

## Building the native lib (`lib/<platform>/libbarretenberg.a`)

The chain build (CGo) links `libbarretenberg.a` for the target platform. Only
`lib/darwin_arm64/` is checked in (for local dev). Validators build the lib for
their platform against v5.0.0:

```bash
# from this directory
CPLUS_INCLUDE_PATH=/opt/homebrew/include \  # macOS only; not needed on Linux
  ./scripts/build-wrapper.sh --platform linux_amd64   # or linux_arm64 / darwin_arm64
```

The script downloads Aztec's pre-built `libbb-external.a` (SHA-verified against
`checksums.json`), recompiles the small C++ wrapper shim, and merges them.
Requires clang++ (C++20), python3, curl, ar, git.

**Do not commit the multi-platform `.a` files raw** (~50 MB each) — build them in
CI/release for the platforms you ship, or store via Git-LFS. `darwin_arm64` is
kept only as a dev convenience.
