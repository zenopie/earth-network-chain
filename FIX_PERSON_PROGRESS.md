# fix/person progress

Audit fixes for personhood/assembly + removal of the transparent gas grant.
Branch `fix/person` off `privacy/orchard`. All done; `go build/vet/test ./...`
and `make genesis-check` pass.

- [x] 1. Assembly (`x/assembly/keeper/subjects.go`): a proposal with a
  revocation (MsgRevokeDsc/MsgRevokeCsca) plus any other message, or with a
  revocation nested in any wrapper (authz MsgExec, ...), is classified as a
  Refusal: no human votes, so it fails. Nested detection is conservative: any
  non-revocation message whose encoding contains a revocation's
  fully-qualified type name.
- [x] 2. Registration:
  - a switch whose idc equals the live registration's idc is refused
    (`ErrRegistrationReplay`, code 1123): a replayed MsgRegister no longer
    zeroes the holder's leaf and restarts activation.
  - `RegistrationBinding` now covers the note ciphertexts (see below), so a
    relayer cannot front-run with garbage ciphertexts. No circuit change: the
    binding is the opaque `address` public input.
- [x] 3. Transparent gas grant removed: `earthd gas-check membership`,
  `Keeper.CheckGasMembership`, `privacy.GasScope`/`GasTransparentSignal`
  (+ vectors), `x/personhood/testdata/gas`, the fixtures script's `gas` step.
  `earthd gas-check registration` stays.
- [x] 4. Sweeps (`x/personhood/keeper/abci.go` runSweeps): expiry, caretaker
  and referrer each have a reserved budget/8 (min 1) share; unused allowance
  carries forward; leftovers go to saturated sweeps in a second round. Total
  per block unchanged.
- [x] 5. Removal-ballot cooldown: `types.RemovalCooldown` = 30 days after a
  ballot on an option closes (`ErrRemovalCooldown`, code 1108). A constant,
  not a param: x/assembly deliberately has no params (gov must not tune the
  chamber that checks it). Carried in genesis (`removal_cooldowns`, field 5).
- [x] 6. Dead `PrivateAnchorAcceptor` hook/branch removed.

## Wallet format change (registration binding)

The passport proof's `address` public input is now

    address = H(TAG_REG, idc, pc_anml, Bytes(ciphertext_anml), pc_erth, Bytes(ciphertext_erth), affiliate)

(was `H(TAG_REG, idc, pc_anml, pc_erth, affiliate)`). `Bytes` is
zk/privacy.Bytes (TAG_BYTES, length, 31-byte chunks), the same as in the
sighash. The wallet must therefore encrypt both notes **before** proving the
passport, and send exactly those ciphertexts in MsgRegister. Pinned vector
(zk/privacy TestRegistrationBindingPinned): idc=1, pc_anml=2,
ct_anml="anml", pc_erth=3, ct_erth="erth", affiliate=0 ->
`0x20ce5fccf5e6e20a8a7b80f7565e41a7c73dbb16ac5e53746e7234ba8b305b0c`.

The sighash format is unchanged.

## Fixtures re-recorded

`scripts/personhood-fixtures.sh <mobile-orch circuits> all`: passports (the
binding changed) and app (passport DSC keys are random, so the app's identity
trees and fee-bundle proofs follow). Verifying keys unchanged.
`networks/genesis.json` rebuilt (assembly `removal_cooldowns: []`).
