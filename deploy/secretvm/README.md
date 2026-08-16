# SecretVM deployment — single-validator earth node

Mirrors `earth-network-backend`: pushing a `v*.*.*` tag has CI build the image,
push it to `ghcr.io/zenopie/earth-network-chain` and rewrite
`docker-compose-secretvm.yaml` with the pinned digest. Tags only — a plain push
to `master` builds nothing.

**`docker-compose-secretvm.yaml` is generated**, so edit the heredoc in the
workflow rather than the file — anything written directly into it is overwritten
by the next build.

    Dockerfile                              builds earthd, carries Ignite + source
    deploy/secretvm/entrypoint.sh           first-boot genesis, then earthd start
    docker-compose-secretvm.yaml            the deployed unit
    .github/workflows/docker-build-secretvm.yml

## Ports

    1317   LCD    the wallet apps and the ads-for-gas backend
    26657  RPC    the explorer's block-range queries

Both are published with explicit host mappings so the addresses are predictable —
`EARTH_NODE_URL` and the apps need to point somewhere fixed. gRPC (9090) and p2p
(26656) are bound inside the container but not published: everything here speaks
REST, and a single validator has no peers to gossip with.

## The volume is not optional

`earth-data:/data` holds genesis, the validator's consensus key and all chain
state. Without it a redeploy does not restart the chain — it creates a *different*
one, with a new genesis and new keys, and every address the apps knew about stops
existing. The entrypoint decides which case it is purely by whether
`/data/config/genesis.json` is there.

## Genesis

`deploy/genesis.json` is generated once with `ignite chain init` and committed.
It carries everything `config.yml` describes — the 539 CSCAs, the seven register
verifying keys, the seeded ANML/ERTH pool, the governance parameters — with two
things removed:

- **the gentx**, because it is bound to a consensus key, and shipping that key in
  a public image would let anyone sign as the validator
- **the dev accounts**, because their keys only ever existed on the machine that
  ran `ignite chain init`, so keeping them means genesis funds nobody can spend

The entrypoint fills both back in with stock `earthd` commands —
`add-genesis-account`, `gentx`, `collect-gentxs` — against a validator key
created on the node. `add-genesis-account` updates auth, bank balances and supply
together, which is why the account is added with the tool rather than by editing
the file.

Regenerate it after any change to `config.yml`:

    ignite chain init --home /tmp/gen --skip-proto
    # then strip gen_txs, the dev accounts, and recompute bank supply

An earlier version ran `ignite chain init` inside the container instead. It
cannot work: init removes and recreates the home directory, and `/data` is a
mount point, so it fails with `Unlinkat //data: device or resource busy`.

## After the first boot

Dev accounts live in the test keyring on the volume:

    earthd keys list --keyring-backend test --home /data
    earthd keys export alice --keyring-backend test --home /data

Fund the ads-for-gas hot wallet from `alice`, and point the backend at this
node's LCD:

    EARTH_NODE_URL=rest+https://<host>:1317

## Devnet posture

`--api.enabled-unsafe-cors` is on so the web app can read the LCD straight from a
browser, and `--minimum-gas-prices 0uerth` accepts zero-fee transactions. Both
are fine for a throwaway chain and both want revisiting before anything real
runs on it.
