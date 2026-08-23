# Proof-of-personhood: status & remaining work

Direction: **Noir / Barretenberg UltraHonk, proved on the handset**, chosen over
Self because the sensitive step never leaves the device (Self offloads it to a
TEE) and because it needs no per-circuit trusted setup. Registration proofs are
bb v5.0.0 UltraHonk proofs (poseidon2 flavor), verified natively on-chain.

The circuits are **ours**, not zkPassport's: `circuits/` in the mobile repo —
`poa_core` plus seven `lean_poa*` variants, one per Document Signer signature
algorithm. What is borrowed from zkPassport is the approach and the proving
library (`noir_android`), not the circuit.

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
   platform lib. **This is the one thing here that blocks launch:** without it
   no Linux validator can build the chain.

2. **On-device proving, end to end on real hardware.** The code is in place —
   `PassportProver` calls `NoirProver` for a bb v5.0.0 poseidon2 UltraHonk proof
   of the `lean_poa` circuit and splits it into the chain's
   `(proof, public_signals)` form. What has not been done is a real passport,
   scanned on a real handset, registering against a real node. Note the version
   lockstep the file warns about: noir_android's bb must match the chain's
   v5.0.0 lib or large-circuit proofs are rejected.

## Settled — do not reopen

- **The public-input schema is pinned and correct.** The positions are
  governance parameters, read in `x/personhood/keeper/registration.go` as
  `params.NullifierIndex`, `params.DscKeyIndex` and `params.CurrentDateIndex`,
  and genesis seeds them **1, 2 and 0**.

  That matches the circuits. `main.nr` takes one public input, `current_date`,
  and returns `(nullifier, dsc_key)`, so Barretenberg's public array is
  `[current_date, nullifier, dsc_key]`. All seven variants — P-256, P-384, the
  three brainpools, RSA-2048 and RSA-4096 — have the identical public surface,
  which is why one set of indices serves every algorithm.

  Confirmed against a real proof rather than by reading the source:
  `x/personhood/keeper/testdata/lean_poa/public_inputs` holds exactly three
  field elements — `250101` (a YYMMDD date), then `expected_nullifier`, then
  `expected_dsc_key`. The phone agrees: `PassportProver.NUM_PUBLIC_INPUTS = 3`,
  split in that order.

  An earlier version of this file said these were placeholders and that getting
  them wrong would invalidate every registration. Neither is true any more, and
  the claim outlived the fix long enough to be repeated as a launch blocker.

- **The verifying keys are seeded, and they are the real ones.** Seven distinct
  keys ship in `deploy/genesis/verifying-keys/`, one per algorithm. The
  `lean_poa` key is byte-identical to the one the keeper test verifies a real bb
  proof against, so these are `bb write_vk` output from the actual circuits, not
  demo keys.

- **`dsc_root` is gone.** It pinned a certificate-registry Merkle root back when
  the registry was off-chain; the field is now `reserved` in
  `proto/earth/personhood/v1/params.proto`. A registration carries the Document
  Signer certificate itself, checked against the CSCA trust store — 539
  certificates, seeded in genesis from `csca/`.
