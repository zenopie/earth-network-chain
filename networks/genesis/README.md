# The launch genesis

`networks/genesis.json` is a **build artifact**. Nobody edits it. It is written by

    scripts/build-genesis.sh

from the sources in this directory, and it is committed so that the file, its
sha256 and the code that produced it all travel together.

    make genesis          rebuild networks/genesis.json + .sha256
    make genesis-check    fail if the artifact no longer matches these sources
    go test ./networks/   check the committed file is self-consistent

## Why it is built rather than edited

A network launch is one genesis file that every node agrees on byte for byte.
Every manual step (stripping a gentx or dev accounts, recomputing the bank
supply) would fail as a mismatched app hash at height 1 on somebody else's
machine rather than as an error here. So `bank.supply` is derived from
`bank.balances`, never written by hand, and `TestConfigYmlAgreesWithGenesisSources`
fails if `config.yml` (the dev chain) disagrees with these sources about
anything both state.

## The sources

| File | What it decides |
| --- | --- |
| `chain.json` | chain id, genesis time, app version, block gas and byte limits — the header a network agrees on before anything else |
| `app_state.json` | every parameter this chain deliberately sets, merged *over* `earthd init`'s defaults |
| `accounts.json` | every balance that exists at height 1, and nothing else may hold one |
| `verifying-keys/*.vk.b64` | one base64 UltraHonk verifying key per passport register circuit (33); the filename is the circuit id. Written by `scripts/privacy-vks.sh` |
| `shielded-verifying-keys/{action,membership,stake,vote}.vk.b64` | x/shielded's keys for every private tx, written by `scripts/privacy-vks.sh` from the circuits (`make privacy-vks-check` verifies them) |
| `gentx/*.json` | signed gentxs to collect. Empty means launching with no validator set |
| `../../csca/` | the CSCA trust store, regenerated through `tools/pki-genesis` |

Everything not named above is whatever `earthd init` produces for the SDK version
this repo builds against. `app_state.json` is a set of *overrides*, not a
complete state, so a field a future SDK adds arrives with its upstream default
instead of silently going missing.

## Changing something

1. Edit the file in this directory.
2. `make genesis`.
3. `go test ./networks/`.
4. Commit the source and the regenerated `networks/genesis.json` together.

Verifying keys are written from the circuits by `scripts/privacy-vks.sh`
(`make privacy-vks`), then `make genesis`; `make privacy-vks-check` fails if a
committed key differs from what the circuits produce. Adding a CSCA means adding the certificate under `csca/` — the
trust store on disk and the one in genesis cannot disagree, because one is
generated from the other.

## The launch ceremony (pending until it has run)

The committed sources are the **placeholder** set: the gentx is signed by a
placeholder account, `accounts.json` still funds two devnet accounts, and
`genesis_time` is a past placeholder. On the operator's machine, one command
turns them into the launch genesis:

    scripts/ceremony.sh --launch <launch.json> --genesis-time <RFC3339> \
      --memo-peer <node id>@<public host>:26656 --moniker <name> \
      [--env-file <file holding VALIDATOR_MNEMONIC>]

The launch identities are the operator's, not this repository's. They come in
the `--launch` file (all four keys required):

    {
      "operator": "earth1…",            the genesis validator's operator account
      "consensus_pubkey": "<base64>",   its ed25519 consensus public key
      "remove_accounts": ["earth1…"],   placeholder accounts to drop (may be [])
      "used_consensus_keys": ["…"]      keys that signed an earlier chain under
                                        this chain id, refused (may be [])
    }

`--memo-peer` (the gentx memo, the genesis's only advertised peer) and
`--moniker` are required: a private, loopback or link-local host and the
placeholder gentx's moniker are refused.

It reads `VALIDATOR_MNEMONIC` (from the environment, else only that line of
`--env-file`), checks it is the launch operator, removes `remove_accounts`,
swaps the placeholder validator account (the committed gentx's signer) for
the operator with the same balance, sets `genesis_time` (UTC, in the future),
signs a new gentx with `consensus_pubkey` (refusing one in
`used_consensus_keys`), rebuilds `networks/genesis.json` and runs
`make genesis-check` and the genesis tests with the ceremony required. Any
failure restores every source. Then commit the sources with the rebuilt
genesis and publish its sha256.

Until then `TestLaunchCeremony` (networks/ceremony_test.go) checks what holds
in both states (one ed25519 gentx signed by its funded operator; the
placeholder gentx, moniker and time together, never a mix) and reports
**PENDING CEREMONY** as a skip. With `EARTH_CEREMONY_CONFIG=<launch.json>` it
also checks the launch identities (the gentx's key, the operator, the removed
accounts); `EARTH_REQUIRE_CEREMONY=1` (what the script and a release build
run) requires that file and fails while the ceremony is pending.

## Still to decide before this is a real launch

These are on the launch checklist ([docs.erth.network](https://docs.erth.network))
and none of them is a thing this script can decide for you:

- **`genesis_time`** must be a real UTC instant near the actual launch (the
  ceremony sets it), and nothing that starts a node may rewrite it at boot.
- `min_deposit`, the minimum gas price and the slashing window are still the
  devnet numbers.
