# SecretVM deployment — single-validator earth node

Mirrors `earth-network-backend`: push a `v*.*.*` tag, CI builds the image, pushes
it to `ghcr.io/zenopie/earth-network-chain` and rewrites
`docker-compose-secretvm.yaml` with the pinned digest.

    Dockerfile                              builds earthd, carries Ignite + source
    deploy/secretvm/entrypoint.sh           first-boot genesis, then earthd start
    docker-compose-secretvm.yaml            the deployed unit
    .github/workflows/docker-build-secretvm.yml

## Ports

    1317   LCD    the wallet apps and the ads-for-gas backend
    26657  RPC    the explorer's block-range queries
    9090   gRPC
    26656  p2p    only needed if a second node ever joins

## The volume is not optional

`earth-data:/data` holds genesis, the validator's consensus key and all chain
state. Without it a redeploy does not restart the chain — it creates a *different*
one, with a new genesis and new keys, and every address the apps knew about stops
existing. The entrypoint decides which case it is purely by whether
`/data/config/genesis.json` is there.

## Why the image is large

It carries the Go toolchain, Ignite and the source tree, and builds genesis on
first boot rather than at build time.

`config.yml` is the source of truth for genesis: 539 CSCAs, seven register
verifying keys, the seeded ANML/ERTH pool, the governance parameters. Baking a
genesis into the image would mean baking the validator's consensus key with it —
the gentx is bound to that key — which puts a signing key in a public image.
Reassembling the same state in the entrypoint instead would mean hand-splicing
bank balances and supply totals, which is the category of change that has broken
a running chain here before while passing every test.

Fresh keys per deployment, no secrets in the image, genesis produced by the same
path used locally. For a devnet that is the cheaper trade.

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
