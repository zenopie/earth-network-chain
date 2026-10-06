# Additional CSCA Certificates

Place manually obtained CSCA certificates here, DER-encoded, named `*.cer`
or `*.der`.

These certificates are loaded in addition to the ICAO master list certificates.
The directory may be empty — the build handles that, and today it is.

**Usage:**
1. Download the CSCA certificate (DER)
2. Save it to this directory as `<name>.cer` or `<name>.der` (e.g. `csca_nigeria.cer`)
3. `make genesis`, then commit the certificate and the regenerated
   `networks/genesis.json` together

**Format:** DER-encoded X.509 certificates
**File extension:** `.cer` or `.der` — `scripts/build-genesis.sh` reads both, and fails on any other file here (this README aside) or on a certificate it cannot parse

**Example certificates to add here:**
- Nigeria CSCA (newer ones not yet distributed)
- Any country-specific CSCAs obtained directly from authorities

## Removed

- **Israel (`EPPCSCA`, PIBA — serials 51, 53 and the 2023 cert).** Held here
  because ICAO does not distribute an Israeli CSCA. Removed deliberately, not
  lost: adding a state's CSCA grants that state the ability to issue passport
  identities the chain will accept as distinct humans, and this one is not
  trusted with that. The consequence is that Israeli passports cannot register,
  because there is no ICAO-distributed Israeli CSCA to fall back to.

  Reversible if that judgement changes: `MsgAddCsca` is authority-gated, so
  governance can add them post-launch (and `MsgRevokeCsca`, also
  authority-gated, withdraws trust from a CSCA's key). The certificates themselves are public and recoverable from git history
  (`git log --diff-filter=D -- csca/additional/`).
