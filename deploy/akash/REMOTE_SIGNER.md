# Remote signer

The consensus key currently lives on the Akash node, at
`/data/config/priv_validator_key.json`. Whoever operates that machine can read
it, and with it double-sign — 5% of stake and a permanent tombstone under this
chain's slashing params.

A remote signer splits the two: the node keeps running on rented hardware, the
key moves to a machine you own. The node asks for signatures; it can no longer
make them.

    home (dynamic IP, no open ports)              Akash
      tmkms ──dials──► cloudflared ──tunnel──►  earthd :26659
       key                                       no key

## Why a dynamic IP is fine

tmkms is the client. CometBFT *listens* on `priv_validator_laddr` and the signer
dials in, so the machine holding the key never needs to be reachable. Both
processes at home make outbound connections only: no port forwarding, no static
address, nothing for a router to be configured about.

Cloudflare supplies the stable hostname on the validator's side, which is the
side that actually needs one.

## Second reason to do it, and the one people underrate

tmkms keeps a state file of the last (height, round, step) it signed and refuses
to sign anything at or below it. That is protection against *equivocation*, not
just theft: if the Akash node is compromised, or you accidentally run a second
validator during a migration, the signer will not produce the conflicting
signature. It cannot sign the thing that gets you slashed.

That state file must be durable and monotonic. Losing it is how a remote signer
causes the exact fault it exists to prevent — which is also why an enclave with
ephemeral storage is a poor place to run one.

## Chain side

`PRIV_VALIDATOR_LADDR` in the SDL's env block turns it on:

    env:
      - PRIV_VALIDATOR_LADDR=tcp://0.0.0.0:26659

The entrypoint writes it into config.toml. Unset, nothing changes and the node
signs locally as it does today.

Then expose 26659 to the tunnel service only — never `global: true`. The signer
authenticates with Secret Connection, so an open port is not immediately fatal,
but there is no reason to offer it to the internet when the tunnel is already
there.

    expose:
      - port: 26659
        to:
          - service: cloudflared

On Cloudflare, add a Public Hostname of type **TCP**:

    signer.erth.network -> tcp://node:26659

## Home side

    cloudflared access tcp --hostname signer.erth.network --url localhost:26659
    tmkms start -c tmkms.toml

tmkms then points at `localhost:26659` and the tunnel carries it.

## Migrating a live validator

Do this before anyone else bonds stake. Moving a consensus key on a running
validator is the single operation most likely to produce an accidental
double-sign, and the window where both the old node and the new signer believe
they may sign is exactly the fault being guarded against.

1. Stop the validator. Confirm it is not producing blocks.
2. Copy `priv_validator_key.json` to the signer host; import it into tmkms.
3. Seed tmkms's state from `priv_validator_state.json` so it does not start
   believing it has signed nothing.
4. Start tmkms, then the node with `PRIV_VALIDATOR_LADDR` set.
5. Confirm blocks are being signed again.
6. **Delete the key from the Akash host.** Skipping this leaves the key on the
   machine the whole exercise was about getting it off.
