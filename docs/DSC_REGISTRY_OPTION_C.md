# Passport PKI: on-chain DSC registry (Option C)

Design for a **trustless, permissionless DSC registry** where the chain verifies the
DSC→CSCA link **natively (in Go), once per DSC** — instead of inside every user's ZK
proof. Users' registration proof stays lean (SOD→DSC + membership), reusing the
existing `lean_poa` circuit. RSA never runs in a phone proof.

> **Status (implemented):** the full stack is built and tested — `zk/poseidon2`
> (Go Poseidon2 matching the circuit), `x/pki/registry` (append-only tree),
> `x/pki/certs` (native multi-curve cert verification, 536/536 real CSCAs), the
> `x/pki` module (`MsgSubmitDSC` + `DscInclusion`/`Root` queries), and the caretaker
> `MsgRegister` binding to the live root history. `tools/pki-genesis` seeds CSCAs from
> an ICAO master list; the mobile client (`chain/Pki.kt`) queries inclusion and submits
> DSCs. Remaining: live-devnet timing validation and per-DSC-algorithm SOD→DSC circuits
> for non-P256 passports (see §12).

## 1. Trust model & why this shape

ICAO passive-authentication chain of trust:

```
CSCA (country root)  --signs-->  DSC (document signer)  --signs-->  SOD (passport)  --hashes-->  DG1 (MRZ)
     |                                  |                                |                            |
 governance-seeded              permissionlessly submitted,       proven in ZK by the         nullifier = Poseidon2(name‖DOB)
 masterlist (rare updates)      chain verifies DSC→CSCA natively   user (SOD→DSC + DG1)        (in-circuit)
```

Where each link is checked:

| Link | Where verified | Cost | Frequency |
|---|---|---|---|
| CSCA is trusted | governance masterlist (on-chain) | — | ~every 3–5 yr |
| DSC→CSCA signature | **native Go, on-chain, once per DSC** | ~ms | once per DSC (permissionless) |
| SOD→DSC signature | **ZK proof (`lean_poa`), per user** | ~13 s on device | once per person |
| DG1/expiry/nullifier | ZK proof | — | per person |

The expensive RSA-4096 work happens **once per DSC, natively**, not in every user's
circuit and not by a trusted curator. DSCs are public data (they ship in every
passport's SOD), so submitting them on-chain leaks nothing; the user's DG1/identity
stays private in the ZK proof.

Result: **fully trustless** (chain verifies every signature), **lean circuit**
(reuse `lean_poa`, no in-circuit RSA), **no privileged curator**, and the only
governance action is maintaining the small, stable CSCA masterlist.

## 2. Components

1. **CSCA masterlist** — the ~536 ICAO country roots, governance-managed. Seeded
   from `earth-network-backend/csca_masterlist/allowlist.ml`. Rarely changes.
2. **DSC registry** — an append-only Poseidon2 Merkle tree of validated DSC public
   keys. Grows via `MsgSubmitDSC`. Its root is what `lean_poa` proves membership in.
3. **`MsgSubmitDSC`** — permissionless; the chain natively verifies DSC→CSCA and
   appends the DSC.
4. **`MsgRegister`** (existing, caretaker) — the user's `lean_poa` proof; now checks
   the proof's `registry_root` against the DSC registry's **root history**.

Proposed home: a dedicated `x/pki` module for the masterlist + DSC tree, with
`x/caretaker` depending on it for the current root(s). (Could also fold into
`x/caretaker`; `x/pki` keeps the PKI concerns separate.)

## 3. State (`x/pki`)

```
Masterlist:      map[cscaKeyId]Csca                 // governance-managed
    Csca = { subject, subjectKeyId, notAfter, pubkeyDER }   // enough to verify a child sig

DscTree:         incremental Merkle tree (Poseidon2, BN254, depth D)
    nodes:       map[(level,index)] -> Field         // sparse; zero-subtree cache for empties
    nextIndex:   uint64
    root:        Field

RootHistory:     ring buffer (or set) of recent roots // for proof freshness (see §7)

DscByFingerprint: set[fingerprint]                    // dedup; fingerprint = sha256(SPKI)
DscLeaves:       map[index] -> DscLeaf                // so clients can fetch a path
    DscLeaf = { keyType, pubkey, leaf, notAfter, fingerprint }
```

Params: tree depth `D`, allowed key types/sizes, root-history length, submit fee.

## 4. Messages

### `MsgSubmitDSC` (permissionless)
```
MsgSubmitDSC { submitter, dsc_cert_der }
```
Handler:
1. `dsc = x509.ParseCertificate(dsc_cert_der)`.
2. Find issuing CSCA in the masterlist by `dsc.AuthorityKeyId` → CSCA `SubjectKeyId`
   (fallback: issuer DN match). Reject if not found.
3. **Native verify** the DSC→CSCA signature:
   `csca.CheckSignature(dsc.SignatureAlgorithm, dsc.RawTBSCertificate, dsc.Signature)`
   — Go handles RSA-PKCS#1v1.5, RSA-PSS, ECDSA, and the various hashes natively.
   (Use the raw `CheckSignature`, not full-chain `Verify`, because ICAO certs often
   violate strict X.509 policy; mirror the AKI/SKI/subject heuristics already in the
   backend's `tools/epassport_verifier.py`.)
4. Check `dsc.NotBefore <= blockTime <= dsc.NotAfter` (not expired).
5. `fingerprint = sha256(dsc.RawSubjectPublicKeyInfo)`; if already present → no-op
   (idempotent), else continue.
6. `leaf = Poseidon2(canonicalPubkeyEncoding(dsc))` — encoding per key type, matching
   the circuit (see §8).
7. Append `leaf` at `nextIndex`; recompute `root` incrementally; push `root` to
   `RootHistory`; store `DscLeaves[nextIndex]`; `nextIndex++`.
8. Emit event `{index, fingerprint, keyType, newRoot}`.

Native RSA/ECDSA verification is microseconds; charge a small fee/gas to deter spam
(the op is idempotent and public, so abuse is limited to storage growth).

### Masterlist management (governance)
`MsgAddCsca` / `MsgRemoveCsca` (gov-gated) or a genesis-seeded masterlist updated via
`MsgUpdateParams`-style governance. Seed from `allowlist.ml` (§10).

## 5. DSC→CSCA native verification notes

- Go `crypto/x509` + `crypto/rsa` + `crypto/ecdsa` cover all algorithms in the store
  (measured: CSCAs are 374 RSA — mostly RSA-4096/6144 — 1 EC P-521; DSCs add RSA-2048
  and ECDSA P-256).
- ICAO certs have quirks (NULL params in AlgorithmIdentifier, non-positive serials,
  SKI/AKI mismatches). The backend already solved these in
  `tools/epassport_verifier.py` — port its matching heuristics.
- RSA-PSS DSCs need `rsa.VerifyPSS` with the right salt/MGF; RSA-PKCS uses
  `rsa.VerifyPKCS1v15`. `x509.CheckSignature` dispatches on the cert's algorithm OID.

## 6. Merkle registry — the one new hard dependency: **Poseidon2 in Go**

The circuit verifies membership with **Poseidon2 (BN254), depth D**, so the on-chain
tree MUST compute identical hashes:
- `leaf = Poseidon2(pubkey field-bytes)`
- `node = Poseidon2([left, right])`

matching `noir-lang/poseidon v0.3.0` (== `@zkpassport/poseidon2`, already validated
against the circuit in JS). **Action item #1:** get a Go Poseidon2-BN254 that matches
(check `gnark-crypto`'s poseidon2 parameters; if they differ, port + validate with the
same vectors we used for JS — e.g. `Poseidon2(64 pubkey bytes)` must equal the
circuit's leaf). Everything else in Option C is standard Cosmos module work; this is
the piece to nail first.

Incremental Merkle tree (Semaphore/Tornado-style): store touched nodes + a per-level
zero-subtree cache; each append is `O(D)` Poseidon2 hashes.

## 7. User registration (caretaker `MsgRegister`) — small change

Today `verifyRegistrationProof` compares the proof's `registry_root` to the static
`params.DscRoot`. Change: compare against the **DSC registry root history**.

Why history: the tree is append-only, so a user builds their proof against whatever
root existed when they fetched their DSC's path; by submit time a newer DSC may have
changed the root. Membership in an older (smaller) append-only tree is still valid, so
accept any root in a recent window:

```
if !pki.RootHistoryContains(proof.registry_root) { reject }
```

Keep a bounded ring buffer (e.g. last N roots or roots within T time). Revoked DSCs
(§9) are the only reason to prune history. Everything else in `verifyRegistrationProof`
(UltraHonk verify, `current_date` pinning, nullifier extraction) is unchanged.

## 8. Circuit requirements

- **`lean_poa` is unchanged** for P-256 DSCs: leaf = `Poseidon2(64-byte x‖y)`, membership
  in the depth-D tree, root = a registry root. It already does SOD→DSC (ECDSA-P256),
  hash bindings, expiry, and the name‖DOB nullifier.
- **Leaf encoding must match the chain per key type.** P-256: `Poseidon2(64 bytes)`.
  RSA: `Poseidon2(modulus limbs/bytes)`. The chain's `canonicalPubkeyEncoding` and the
  circuit's leaf computation must agree for each type.
- **Per-DSC-algorithm circuits (unavoidable, but minimal).** The ZK proof must verify
  the SOD signature, whose algorithm is the DSC's. So we need one circuit per DSC
  signature algorithm we support: P-256 (done), then RSA-2048, RSA-4096, P-384, … Note
  this set is **additive** and only on the DSC side — Option C needs **no CSCA-side
  circuits at all** (that link is native Go). Each is registered as its own
  `verifying_keys[algo]` on-chain (already supported).
- **Tree depth `D`.** `lean_poa` currently uses 16 (65 536 DSCs). Consider `D = 20`
  (~1 M) for headroom; costs 4 extra Poseidon2 per proof. Decide before seeding.

## 9. Revocation (v1.1)

Append-only membership means a revoked/compromised DSC stays valid unless handled. Add
a revocation set + prune affected roots from history, or bind a `revocation_tree_root`
(as zkpassport does) and check non-revocation in-circuit or natively. Out of scope for
v1; call it out so it isn't forgotten.

## 10. Genesis / seeding

- Extract the 536 CSCAs from `csca_masterlist/allowlist.ml`
  (`tools/extract_csca.extract_csca_ders`) → seed the masterlist at genesis (or via an
  initial governance batch).
- DSC tree starts empty; it fills permissionlessly via `MsgSubmitDSC`. Optionally
  pre-seed known DSCs (ICAO PKD / harvested) so early users don't each pay to submit.
- The off-chain `tools/registry-builder` (already written + circuit-validated) remains
  useful for test fixtures and to cross-check the on-chain tree root.

## 11. Client (app) flow

1. Scan passport → DG1 + SOD → extract DSC.
2. Query `x/pki`: `DscInclusion(pubkey) → { root, index, pathBits[D], siblings[D] }`.
   - If absent → send `MsgSubmitDSC(dsc_cert_der)` (anyone can; verified natively),
     then re-query.
3. Build `lean_poa` inputs (existing `PassportInputs.leanInputs`) with the returned
   `root`/`pathBits`/`siblings` (no Poseidon2 in the app — the chain serves the path).
4. Prove on device (~13 s) → `MsgRegister`.

## 12. Implementation phases

1. ✅ **Poseidon2-BN254 in Go** (`zk/poseidon2`), validated against the circuit's
   leaf + node hashes (§6).
2. ✅ **`x/pki` module**: masterlist state + governance msgs; DSC incremental Merkle tree;
   `MsgSubmitDSC` with native cert verification; root history; inclusion query.
3. ✅ **Seed masterlist** from `allowlist.ml` — `certs.ParseMasterList` + `tools/pki-genesis`
   (extracts 536 CSCAs).
4. ✅ **Wire caretaker**: `verifyRegistrationProof` checks `registry_root ∈ pki root history`
   (falls back to static `params.DscRoot` only when no pki keeper is wired).
5. ✅ **Client** (`earth-network-mobile`): `chain/Pki.kt` inclusion query + `MsgSubmitDSC`;
   `PassportInputs.scannedDsc` + scanner wiring.
6. ⏳ **Per-DSC-algorithm circuits**: P-256 done (`lean_poa`); RSA-2048/4096 and Brainpool
   SOD→DSC variants as coverage needs grow (additive, DSC-side only).
7. ⏳ **Live-devnet validation**: submit→inclusion timing against `ignite chain serve`.

## Open decisions (resolved defaults in parentheses)
- Tree depth `D` (**16** — matches the circuit; raise with a coordinated recompile).
- `x/pki` as its own module vs folded into `x/caretaker` (**own module**).
- Root-history size / policy (**64**, ring-buffer pruning in `recordRoot`).
- Pre-seed DSCs at genesis vs purely permissionless (**permissionless**; genesis seeds
  only CSCAs).
- Revocation design (**v1.1** — revocation tree / governance removal + history prune).
