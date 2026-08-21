# Proof-of-personhood: status & remaining work

Direction: **zkPassport** (Noir / Barretenberg **UltraHonk**), chosen over Self
because zkPassport proves the sensitive step **on-device** (Self offloads to a
TEE) and needs **no per-circuit trusted setup**. Registration proofs are bb
v5.0.0 UltraHonk proofs (poseidon2 flavor), verified natively on-chain.

## Done
- [x] **On-chain verifier** — `zk/ultrahonk`: CGo bindings to Barretenberg's
      own verifier (`third_party/barretenberg-go`, native lib rebuilt against bb
      **v5.0.0**). Verifies real bb 5.0.0 UltraHonk proofs; sound (rejects
      tampered). Poseidon2 flavor. See `third_party/barretenberg-go/README.md`.
- [x] **Caretaker wired** — `MsgRegister{proof, public_signals, signature_algorithm}`
      → `verifyRegistrationProof` selects the bb VK from `params.verifying_keys`,
      calls `ultrahonk.Verify`, checks the `dsc_root` binding, returns the
      nullifier. Keeper test green against the real proof.
- [x] **Build infra** — `third_party/barretenberg-go/scripts/build-wrapper.sh` +
      `checksums.json` pinned to v5.0.0 for all platforms.

## Remaining
1. **Validator native libs** — only `darwin_arm64` is checked in (dev). Build
   `lib/linux_amd64/` and `lib/linux_arm64/` against v5.0.0 in CI/release via
   `third_party/barretenberg-go/scripts/build-wrapper.sh --platform linux_amd64`
   (runs on Linux; SHA-pinned). The chain build needs `CGO_ENABLED=1` + the
   platform lib.
2. **Pin the public-input schema** — ~~`verifyRegistrationProof` currently uses
   placeholder indices.~~ No longer hardcoded: the positions are governance
   parameters, read at `x/personhood/keeper/registration.go:43,74,112` as
   `params.NullifierIndex`, `params.DscKeyIndex` and `params.CurrentDateIndex`.
   Genesis seeds them 1, 2 and 0.
   What remains is choosing the right values, not changing code — and getting
   them wrong at launch is recoverable by proposal rather than invalidating every
   registration made so far. Set them to zkPassport's actual register/outer-
   circuit positions once that circuit is fixed.
3. **On-device prover + proof flavor** — the mobile app must generate a **bb
   v5.0.0 poseidon2** UltraHonk proof via zkPassport's prover (Noir witness +
   Barretenberg), *not* their EVM/keccak proof. The `MsgRegister` wire shape
   already fits (proof bytes + public_signals + algorithm); `PassportProver` must
   emit that proof. See the mobile repo's `PassportProver.kt`.
4. **Seed VKs** — governance sets `params.verifying_keys[<algo>]` to the bb VK
   (`bb write_vk` bytes) per supported document/signature circuit, and
   `params.dsc_root` to the trusted certificate-registry root.
