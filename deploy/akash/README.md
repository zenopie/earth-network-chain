# Akash deployment — earth devnet

One deployment, three services, one image.

    node          earthd, the single validator          -> /data
    app           the ads-for-gas service               -> /app/state
    cloudflared   Cloudflare Tunnel connector

`node` and `app` are the same container run with different entrypoints; the
Dockerfile at the repo root builds both payloads. That is why there is one image
to build, one digest to pin, and one release.

    deploy/akash/deploy.yaml       the deployed unit
    deploy/docker/entrypoint.sh    node: first-boot genesis, then earthd start
    deploy/docker/backend-entrypoint.sh   app: uvicorn
    backend/                       the ads-for-gas source

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

    GAS_WALLET_MNEMONIC   spendable ERTH; must never reach the repository
    TUNNEL_TOKEN          anyone holding it can attach a replica to your tunnel

Both still reach the provider — everything in a submitted SDL does. What the
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

**One tunnel is enough, and that is a consequence of one deployment.** A tunnel's
replicas are chosen by proximity with no traffic steering, so connectors able to
reach different origins would black-hole requests non-deterministically. Here a
single connector reaches both origins by service name, so the question does not
arise. Configure the Public Hostnames on Cloudflare's side:

    lcd.* -> http://node:1317      rpc.* -> http://node:26657
    ads.* -> http://app:8000

`app` reaches the chain at `rest+http://node:1317` over the cluster network. Do
not point it at the provider's public hostname: on the provider hosting it, that
is a hairpin back to its own NodePort and it hangs rather than failing. cosmpy
builds its LedgerClient inside FastAPI's startup event, so uvicorn never finishes
starting — the port completes a TCP handshake and then never answers, while the
lease still reports ready, because nothing defines a readiness probe.

## AdMob

`ADMOB_AD_UNIT_ID` is the mobile app's `REWARDED_AD_UNIT_ID` (`HostActivity.kt`),
the unit whose `ServerSideVerificationOptions` carry the wallet address as
`custom_data`. The interstitial unit beside it never calls back. Unset is not a
safe default: the service starts and skips the check, letting a valid Google
signature from any of your ad units claim a grant.

Point the SSV callback at the tunnel hostname, not a mapped port — a dead
callback URL fails silently: Google records a delivery, the user watched an
advert, and nothing arrives.

## Devnet posture

`--api.enabled-unsafe-cors` and `MIN_GAS_PRICES=0uerth`. Fine for a throwaway
chain; both want revisiting before anything real. Zero-fee plus open CORS on a
public endpoint is a free write-amplification target, and the gas burn that makes
fees deflationary collects nothing to burn.

The hot wallet holds 10,000 ERTH from genesis — 200,000 grants at
`DUST_UERTH=50000`. `/health` reports `grants_remaining`; when it runs dry the
failure is silent and expensive.
