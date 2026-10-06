# CSCA Master List

The ICAO master list, the bulk of the CSCA trust store.

**Current file:** `allowlist.ml`
- Source: https://github.com/zenopie/csca-trust-store
- Contains: 536 CSCA certificates (366 signing keys) from various countries
- Last updated: check git history

## Updating the Master List

1. Download the latest `allowlist.ml` from the trust store repository
2. Replace the file in this directory
3. `make genesis` (it runs `tools/pki-genesis` over this file and
   `../additional/*.cer` into `pki.cscas`), then commit the file and the
   regenerated `networks/genesis.json` together

The chain reads CSCAs from genesis (and from governance's `MsgAddCsca`), never
from this file at runtime.
