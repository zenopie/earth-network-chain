# CSCA trust store

The root of trust for passport verification, and the input that produced the
`pki.cscas` block in `config.yml`.

A Country Signing Certificate Authority is what a state uses to sign the
Document Signer certificates that in turn sign passports. `x/pki` will only
accept a DSC that chains to one of these, so this directory decides which
passports can ever register. Nothing else the chain does is as load-bearing.

    masterlist/allowlist.ml   ICAO master list — 536 CSCAs
    additional/*.cer          3 Israeli CSCAs ICAO does not distribute

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
