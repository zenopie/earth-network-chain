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
| `shielded-verifying-keys/{transfer,membership}.vk.b64` | x/shielded's keys for every private tx, written by `scripts/privacy-vks.sh` from the circuits (`make privacy-vks-check` verifies them) |
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

## The launch ceremony (pending until it has run)

The committed sources are the **placeholder** set: the gentx is signed by the
placeholder account `earth14e6s…` (already with the launch consensus key
`PGqvPN4C…`, which never signed any chain), `accounts.json` still funds the
devnet faucet `earth1s7rgs…` and the ads-for-gas wallet `earth1jtc2z…`, and
`genesis_time` is a past placeholder. On the operator's machine, one command
turns them into the launch genesis:

    scripts/ceremony.sh --genesis-time <RFC3339> --pubkey '{"@type":"/cosmos.crypto.ed25519.PubKey","key":"PGqvPN4CxEkxvvh3tSBX0SGeBgjMdqQwZkdHt8FRLm4="}'

It reads `VALIDATOR_MNEMONIC` (from the environment, else only that line of
the deploy repo's `.env`, or `--env-file`), checks it is the launch operator
`earth1n6amvkgfrrgy6ulhurewnm0endkgye69fkcapr`, removes the two devnet
accounts, swaps the validator account for the operator with the same 1,000
ERTH, sets `genesis_time` (UTC, in the future), signs a new gentx with the
given consensus key (refusing one that signed an earlier earth-1), rebuilds
`networks/genesis.json` and runs `make genesis-check` and the genesis tests
with the ceremony required. Any failure restores every source. Then commit
the sources with the rebuilt genesis and publish its sha256.

Until then `TestLaunchCeremony` (networks/ceremony_test.go) checks what holds
in both states (one gentx, the launch consensus key, signed by its funded
operator; exactly the placeholder set, never a mix) and reports **PENDING
CEREMONY** as a skip; `EARTH_REQUIRE_CEREMONY=1 go test ./networks/` (what
the script and a release build run) fails instead.

## Still to decide before this is a real launch

These are in `docs/LAUNCH_CHECKLIST.md` and none of them is a thing this script
can decide for you:

- **`genesis_time`** must be a real UTC instant near the actual launch (the
  ceremony sets it), and the container entrypoint must not rewrite it at boot.
- `min_deposit`, the minimum gas price and the slashing window are still the
  devnet numbers.
