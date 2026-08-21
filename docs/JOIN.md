# Running an Earth node

Everything you need to sync a node on `earth-1`.

If you can't get a node syncing from this page alone, that's a bug in this page —
please open an issue.

> **Not launched yet.** Three values below are marked `TBD` and will be filled in
> with the launch release: the seed address, the genesis hash, and the chain's
> start time. Until then the network is a single validator and there is nothing
> to join.

---

## 1. Get the binary

Download from the [latest release](https://github.com/zenopie/earth-network-chain/releases/latest):

```bash
VERSION=v0.1.7          # use the launch tag
ARCH=amd64              # or arm64

curl -LO https://github.com/zenopie/earth-network-chain/releases/download/$VERSION/earthd_${VERSION}_linux_${ARCH}.tar.gz
curl -LO https://github.com/zenopie/earth-network-chain/releases/download/$VERSION/checksums.txt

sha256sum -c checksums.txt --ignore-missing     # must say OK
tar xzf earthd_${VERSION}_linux_${ARCH}.tar.gz
sudo install earthd_${VERSION}_linux_${ARCH}/earthd /usr/local/bin/
```

Check it:

```bash
earthd version --long
```

The `version` and `commit` it prints are how you answer "am I running what
everyone else is running" — the only question that matters during an upgrade.

**Building instead of downloading?** You need cgo and the proof verifier:

```bash
sudo apt-get install -y clang python3 binutils libc++-dev libc++abi-dev
cd third_party/barretenberg-go && ./scripts/build-wrapper.sh --platform linux_amd64
cd ../.. && make install
```

There is also a container image at `ghcr.io/zenopie/earth-network-chain`, pinned
by digest. See [deploy/docker/README.md](../deploy/docker/README.md).

---

## 2. Initialise

```bash
earthd init "<your-moniker>" --chain-id earth-1
```

---

## 3. Install genesis — and check it

This is the step that decides whether you join `earth-1` or start your own chain
alone. A node whose genesis differs by one byte computes a different app hash and
will never agree with anyone.

```bash
curl -L -o ~/.earth/config/genesis.json \
  https://github.com/zenopie/earth-network-chain/releases/download/$VERSION/genesis.json

sha256sum ~/.earth/config/genesis.json
```

It must print:

```
TBD — published with the launch release
```

If it doesn't, stop. Don't work around it.

```bash
earthd genesis validate-genesis
```

---

## 4. Configure

**Seeds** — in `~/.earth/config/config.toml`:

```toml
seeds = "TBD@seed.erth.network:26656"
```

**Minimum gas price** — in `~/.earth/config/app.toml`. **Required. The node will
not start without it**, and the error doesn't say which file to edit:

```
set min gas price in app.toml or flag or env variable
```

Below this value your node won't relay a transaction:

```toml
minimum-gas-prices = "TBD"    # the launch release will name the figure
```

**Pruning** — pick by what the node is for:

| role | setting |
| --- | --- |
| validator | `pruning = "default"` |
| public RPC | `pruning = "custom"`, `pruning-keep-recent = "362880"`, `pruning-interval = "100"` |
| archive | `pruning = "nothing"` — grows without limit |

**Don't** enable `enabled-unsafe-cors` on a validator. It lets any website read
your node and broadcast through it. If you're serving a browser app, run a
separate read-only node for that.

---

## 5. Start

```bash
earthd start
```

Watch it catch up:

```bash
curl -s localhost:26657/status | jq .result.sync_info
```

`catching_up: false` means you're synced.

---

## Hardware

| | validator | public RPC | archive |
| --- | --- | --- | --- |
| CPU | 4 cores | 4 cores | 8 cores |
| RAM | 16 GB | 16 GB | 32 GB |
| Disk | 500 GB SSD | 1 TB SSD | 2 TB+ SSD |

SSD, not spinning disk — the node fsyncs every block.

**One thing specific to this chain:** every passport registration verifies a
zero-knowledge proof on-chain, which is CPU-heavy and cannot be skipped. Budget
more CPU than a chain of this size would normally need.

---

## Becoming a validator

Sync first. A node that isn't caught up can't validate.

Write `validator.json`:

```json
{
  "pubkey": PASTE_OUTPUT_OF_show-validator,
  "amount": "1000000uerth",
  "moniker": "<your-moniker>",
  "commission-rate": "0.1",
  "commission-max-rate": "0.2",
  "commission-max-change-rate": "0.01",
  "min-self-delegation": "1"
}
```

`pubkey` is the whole JSON object from `earthd comet show-validator`, pasted in
unquoted — not a string.

```bash
earthd tx staking create-validator validator.json \
  --chain-id earth-1 --from <your-key> --gas auto --gas-adjustment 1.5
```

**Your consensus key should not live on the node.** Use a remote signer — see
[deploy/akash/REMOTE_SIGNER.md](../deploy/akash/REMOTE_SIGNER.md). It fails closed: with
a signer configured and none answering, your node signs nothing rather than
signing with a key it shouldn't have.

Double-signing gets you slashed and tombstoned permanently. Never run two nodes
with the same consensus key — not during a migration, not for a few seconds.

---

## Upgrades

Upgrades halt the chain at an agreed height. Your node stops on its own and waits.

Use [cosmovisor](https://docs.cosmos.network/main/build/tooling/cosmovisor) so the
new binary is staged in advance and swaps automatically, instead of you doing it
by hand at whatever hour the height lands.

Each upgrade's release notes give the name, the height, and the binary.

---

## If something goes wrong

**"expected chain id earth-1"** — wrong genesis. Redo step 3.

**Wrong app hash at a height** — your genesis differs from everyone else's, or
you're on the wrong binary. Check both hashes.

**"set min gas price in app.toml"** — the node won't start until
`minimum-gas-prices` is set in `app.toml`. See step 4.

**No peers** — your seeds are wrong, or port 26656 isn't reachable. Peers have to
be able to dial you.

**Stuck at a height with peers connected** — usually an upgrade you haven't
applied. Check the releases page.
