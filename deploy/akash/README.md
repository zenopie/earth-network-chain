# Akash deployment — earth chain node

Two services in one deployment: the validator and a Cloudflare Tunnel connector.

    node          earthd, the single validator   -> /data
    cloudflared   Cloudflare Tunnel connector

The ads-for-gas service is a separate repo, image and lease
(`earth-network-backend`), with its own tunnel. Closing a lease destroys its
volumes, and this one holds the chain's state — a backend change must not be
able to take it.

    deploy/akash/deploy.yaml       the deployed unit
    deploy/docker/entrypoint.sh    first-boot genesis, then earthd start

## Build the image first

CI builds and pushes on `v[0-9]+.[0-9]+.[0-9]+` tags only — a push to `master`
builds nothing — and then rewrites both `image:` lines in `deploy.yaml` with the
digest it just pushed and commits that back to master.

    git tag v0.1.0 && git push origin v0.1.0
    git pull origin master     # picks up the pinned digest

So after a release, pull and deploy: the SDL already points at the bytes CI
built. The package must be public on ghcr.io, or the provider cannot pull it.

## Deploy

Secrets are NOT in the SDL. Two values are injected into the submitted copy from
the gitignored `.env` at the repo root:

    TUNNEL_TOKEN          anyone holding it can attach a replica to your tunnel

It still reaches the provider — everything in a submitted SDL does. What the
injection avoids is them reaching a public repository.

    POST /v1/deployments   {"data": {"sdl": "<sdl>", "deposit": 5}}
    GET  /v1/bids/{dseq}
    POST /v1/leases        {"manifest": "<from create>", "leases": [...]}

Auth is `x-api-key` (Console API, managed wallet / credit card). There is no
`provider-services login` — that CLI signs with a local key and has no session.

## One lease holds both volumes

Closing it destroys the chain's state — genesis, the validator's consensus key,
every account — and the replay database with it. The next deploy is not a
restart; it is a different chain.

Image and env changes go in place with `PUT /v1/deployments/{dseq}` and keep the
volumes. Structural changes do not: endpoint kinds and resources are part of what
the provider bid on, and the API rejects them with `over-utilized PORT
endpoints`. Those need a close-and-recreate.

If you are on an Akash trial, deployments auto-close after 24 hours, which is the
same thing.

## Addresses

Every port is exposed both `global` (a mapped port in the 30000-32767 range,
read from the lease status) and to `cloudflared`. The mapped ports are the escape
hatch that keeps working if the tunnel is misconfigured; the tunnel is what
survives a lease change, since Akash reassigns external ports every time.

Not `as: 80`. That asks the provider for an HTTP ingress on a generated
hostname; on the first lease of this deployment the pod went ready and served
RPC while the hostname returned nginx 404 for ten minutes, indistinguishable
from a hostname that was never registered. Mapped ports came up immediately.

**One tunnel per deployment, not per service.** A tunnel's replicas are chosen
by proximity with no traffic steering, so connectors able to reach different
origins would black-hole requests non-deterministically. This tunnel serves this
deployment; the backend has its own. Configure the Public Hostnames on
Cloudflare's side:

    lcd.* -> http://node:1317      rpc.* -> http://node:26657

The backend must NOT be leased on the same provider as this node. Its
`EARTH_NODE_URL` is a provider hostname and NodePort, and from inside that same
provider's cluster it is a hairpin back to itself which hangs rather than
failing — cosmpy builds its LedgerClient inside FastAPI's startup event, so
uvicorn never finishes starting, the port completes a TCP handshake and then
never answers, and the lease still reports ready because nothing defines a
readiness probe. Pointing it at this tunnel's `lcd.*` hostname avoids the
question entirely.

## Devnet posture

`--api.enabled-unsafe-cors` and `MIN_GAS_PRICES=0uerth`. Fine for a throwaway
chain; both want revisiting before anything real. Zero-fee plus open CORS on a
public endpoint is a free write-amplification target, and the gas burn that makes
fees deflationary collects nothing to burn.

`deploy/genesis.json` seeds the ads-for-gas hot wallet
(`earth1jtc2zjmmmyttdayz6aw8vfgt5qn4hg7rpxaar6`) with 10,000 ERTH and the dev
handset with 100,000. Both are devnet keys; drop them before anything real.
