# Akash deployment — single-validator earth devnet

Same image and the same `deploy/docker/entrypoint.sh` as the docker-compose
deployment; only the hosting primitives differ. `deploy.yaml` is the SDL.

    deploy/akash/deploy.yaml        the deployed unit
    deploy/docker/entrypoint.sh   first-boot genesis, then earthd start
    deploy/genesis.json             the chain state genesis carries
    Dockerfile                      builds earthd

## Build the image first

**The tag in `deploy.yaml` does not exist until you make it.** CI builds and
pushes only on `v[0-9]+.[0-9]+.[0-9]+` tags — a push to `master` builds nothing —
so an image published before the staking change carries the *old* tokenomics:
the emission compounded into bonded stake, no claimable rewards, a 2% community
tax. Deploying `v0.0.7` gets you that chain, not this one.

    git tag v0.0.8 && git push origin v0.0.8

The workflow pins `deploy.yaml` for you: it rewrites the `image:` line with the
digest it just pushed and commits that back to master alongside
`docker-compose.yaml`. So after a release, pull master and deploy — the SDL is
already pointing at the bytes CI built.

    git pull origin master

A tag can be moved to point at different bytes later. A digest cannot, and on a
chain the difference between "the image I tested" and "an image with that name"
is a consensus failure — which is why the workflow fails the release outright if
its pattern stops matching the `image:` line, rather than leaving a stale pin
behind a green build.

The package must be public on ghcr.io, or the provider cannot pull it — Akash
has nowhere to put registry credentials.

## Deploy

Needs the `provider-services` CLI, a funded AKT wallet (~5 AKT covers the
deposit), and a client certificate created once per key.

    provider-services tx cert generate client --from <key>
    provider-services tx cert publish client --from <key>

    provider-services tx deployment create deploy/akash/deploy.yaml --from <key>
    provider-services query market bid list --owner <addr> --dseq <dseq>
    provider-services tx market lease create --dseq <dseq> --provider <provider> --from <key>
    provider-services provider send-manifest deploy/akash/deploy.yaml --dseq <dseq> --provider <provider> --from <key>

## Endpoints

    provider-services lease-status --dseq <dseq> --provider <provider> --from <key>

LCD is exposed `as: 80`, which is what earns a provider hostname instead of a
random high port — so it comes back as a plain URL and is the address worth
handing to the apps:

    EARTH_NODE_URL=rest+http://<hostname>

RPC gets an assigned external port in the 30000-32767 range, printed by
`lease-status` as `forwarded_ports`. It is stable for the life of the lease and
changes if the lease is recreated, so read it rather than assuming it.

p2p (26656) and gRPC (9090) are not exposed. A single validator has no peers to
gossip with, and everything here speaks REST.

For addresses that survive a lease being recreated, put a domain in front:
add `accept: [rpc.example.com]` to the exposed port and point a DNS A record at
the provider's IP.

## Closing the lease destroys the chain

The persistent volume survives container restarts and deployment *updates*. It
does not survive `deployment close`. When the lease ends the volume goes with
it, and the next deploy is not a restart — it is a different chain, with a new
genesis, a new validator key, and every address the apps knew about gone.

This is the same failure `deploy/docker/README.md` describes for a missing
volume, with a different trigger. The entrypoint tells the two cases apart purely by whether
`/data/config/genesis.json` is there.

`count: 1` matters for the same reason. A second replica is not a second node on
this network; it is a second chain booting the same genesis with its own
validator key, diverging from the first block it signs.

## First boot

The validator key is generated on the node and its mnemonic printed exactly once
into the container log:

    provider-services lease-logs --dseq <dseq> --provider <provider> --from <key>

Capture it then or not at all. `VALIDATOR_MNEMONIC` in the SDL would let you
supply the key instead, and is deliberately not set: everything in `deploy.yaml`
is visible to the provider hosting the lease.

You mostly should not need it. `deploy/genesis.json` seeds the development
handset with 100k ERTH so a fresh deployment is testable without the validator's
key. Drop that account before anything real runs on this chain.

Keys live in the test keyring on the volume:

    earthd keys list --keyring-backend test --home /data

## genesis_time is stamped at first boot

`deploy/genesis.json` carries the timestamp of whichever machine ran `ignite
chain init`. CometBFT gives block 1 exactly that time while block 2 gets the
wall clock, and the emission is prorated against elapsed time — so the entire
gap pays out in a single block. Deploying the committed genesis a day after it
was generated minted **125,485 ERTH at height 2**, and the lump grows for as
long as the file sits in the repo unchanged.

The entrypoint now rewrites `genesis_time` to the current time before creating
the validator, so a fresh chain starts at zero regardless of how old the file
is. Nothing to do per-deploy; it is worth knowing because the symptom — a
supply that is wrong from the second block, before any transaction — looks
nothing like its cause.

## Devnet posture

`--api.enabled-unsafe-cors` is on so the web app can read the LCD straight from
a browser, and `MIN_GAS_PRICES=0uerth` accepts zero-fee transactions. Both are
fine for a throwaway chain and both want revisiting before anything real runs on
it. Note that zero-fee plus open CORS on a public endpoint is a free
write-amplification target — the gas burn that makes fees deflationary collects
nothing to burn.
