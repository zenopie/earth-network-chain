# CSCA trust store

The root of trust for passport verification, and the input that produced the
`pki.cscas` block in `config.yml`.

A Country Signing Certificate Authority is what a state uses to sign the
Document Signer certificates that in turn sign passports. `x/pki` will only
accept a DSC that chains to one of these, so this directory decides which
passports can ever register. Nothing else the chain does is as load-bearing.

    masterlist/allowlist.ml   ICAO master list — 536 CSCAs
    additional/*.cer          hand-added CSCAs ICAO does not distribute (none)

`additional/` is currently empty, so the ICAO master list is the entire trust
store. It held three Israeli CSCAs until they were removed on purpose — a CSCA
is a key that can mint passport identities this chain accepts, so which states
are on this list is a sybil-resistance decision, not a formality. The cost of
that particular removal is that Israeli passport holders cannot register at all,
since ICAO does not distribute an Israeli CSCA to fall back to.

Regenerate the genesis block with:

    go run ./tools/pki-genesis \
      csca/masterlist/allowlist.ml \
      csca/additional/*.cer

Certificates that share a signing key are intentionally **not** collapsed: the
keeper indexes issuers by subject DN as well as by SKI, and one SKI here appears
under two distinct DNs.

These lived in `earth-network-backend` while registration was verified by a
server. Registration is now proved on-device and verified on-chain, so the trust
store belongs with the chain that enforces it.

The parsing tests are opt-in, since they read the master list directly. The path
must be absolute — `go test` runs with the package directory as its working
directory, so a repo-relative one will not resolve:

    MASTERLIST_ML="$PWD/csca/masterlist/allowlist.ml" go test ./x/pki/certs/...
