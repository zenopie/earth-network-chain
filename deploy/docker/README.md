# Container deployment — single-validator earth node

The image and the entrypoint here are the deployment. `docker-compose.yaml` runs
them on a plain Docker host or on SecretVM; `deploy/akash/` runs the same image
on Akash and shares this entrypoint. Anything below that is not about compose
specifically applies to both.

Mirrors `earth-network-backend`: pushing a `v*.*.*` tag has CI build the image,
push it to `ghcr.io/zenopie/earth-network-chain` and rewrite
`docker-compose.yaml` with the pinned digest. Tags only — a plain push
to `master` builds nothing.

**`docker-compose.yaml` is generated**, so edit the heredoc in the
workflow rather than the file — anything written directly into it is overwritten
by the next build.

The same release step also pins `deploy/akash/deploy.yaml`, but that file is
hand-maintained rather than generated: only its `image:` line is rewritten, and
everything else you put there survives.

    Dockerfile                          builds earthd on a slim runtime
    deploy/docker/entrypoint.sh         first-boot genesis, then earthd start
    docker-compose.yaml                 the deployed unit
    .github/workflows/docker-build.yml  builds and pins the digest

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

Two accounts are seeded with 100k ERTH each so a fresh deployment is testable
without hunting for the validator's mnemonic: the development handset, and the
ads-for-gas hot wallet. Both are devnet keys with no value; drop them from
`deploy/genesis.json` for anything real.

`genesis_time` is stamped to the current time by the entrypoint before the
validator is created. The committed file carries the timestamp of the machine
that generated it, and CometBFT gives block 1 exactly that time while block 2
gets the wall clock — so the emission, prorated against elapsed time, would pay
the whole gap out in a single block. Left alone, a genesis committed a day
earlier minted 125,485 ERTH at height 2, and the lump grew for as long as the
file sat unchanged.

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
