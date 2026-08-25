# The launch genesis

`networks/genesis.json` is a **build artifact**. Nobody edits it. It is written by

    scripts/build-genesis.sh

from the sources in this directory, and it is committed so that the file, its
sha256 and the code that produced it all travel together.

    make genesis          rebuild networks/genesis.json + .sha256
    make genesis-check    fail if the artifact no longer matches these sources
    go test ./deploy/...  check the committed file is self-consistent

## Why it is built rather than edited

A network launch is one genesis file that every node agrees on byte for byte.
The previous process was `ignite chain init`, then hand-stripping the gentx and
the dev accounts, then "recompute bank supply" — three manual steps, each of
which fails as a mismatched app hash at height 1 on somebody else's machine
rather than as an error here.

Two things this already caught:

- `config.yml` and `networks/genesis.json` had disagreed about the shape of the
  token supply since commit `6dd49f3` — the pre-mine was split a third to the
  ANML/ERTH pool and two thirds to the liquidity auction in one file and not the
  other. `TestConfigYmlAgreesWithGenesisSources` now fails on that.
- `bank.supply` was maintained separately from `bank.balances`. It is now derived
  from them and never written by hand.

## The sources

| File | What it decides |
| --- | --- |
| `chain.json` | chain id, genesis time, app version — the header a network agrees on before anything else |
| `app_state.json` | every parameter this chain deliberately sets, merged *over* `earthd init`'s defaults |
| `accounts.json` | every balance that exists at height 1, and nothing else may hold one |
| `verifying-keys/*.vk.b64` | one base64 UltraHonk verifying key per register circuit; the filename is the circuit id |
| `gentx/*.json` | signed gentxs to collect. Empty means launching with no validator set |
| `../../csca/` | the CSCA trust store, regenerated through `tools/pki-genesis` |

Everything not named above is whatever `earthd init` produces for the SDK version
this repo builds against. `app_state.json` is a set of *overrides*, not a
complete state, so a field a future SDK adds arrives with its upstream default
instead of silently going missing.

## Changing something

1. Edit the file in this directory.
2. `make genesis`.
3. `go test ./deploy/...`.
4. Commit the source and the regenerated `networks/genesis.json` together.

Swapping a verifying key is a file drop: overwrite `verifying-keys/<circuit>.b64`
and rebuild. Adding a CSCA means adding the certificate under `csca/` — the
trust store on disk and the one in genesis cannot disagree, because one is
generated from the other.

## Still to decide before this is a real launch

These are in `docs/LAUNCH_CHECKLIST.md` and none of them is a thing this script
can decide for you. The values here are today's devnet values:

- **`genesis_time`** is the timestamp of the machine that first generated the
  file. It must be a real UTC instant near the actual launch, and the container
  entrypoint has to stop rewriting it at boot — that rewrite is why no two nodes
  can currently share a chain.
- **`gentx/` is empty**, so the validator set at height 1 is empty. Either
  collect gentxs from the launch operators or launch and take
  `MsgCreateValidator` after height 1 — both work, leaving it implicit does not.
- **The one keyed account is a devnet key.** `earth1jtc2zj…` is the ads-for-gas
  hot wallet and its key has no value. Replace it with real operational funding
  from a key that has never been on a laptop.
- **`app_version` is `0`.** Set it to the launch app version.
- **The verifying keys are placeholders**, and `min_deposit`, the minimum gas
  price and the slashing window are all still devnet numbers.
