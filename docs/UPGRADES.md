# Upgrading the chain

How a coordinated upgrade runs, and the things that go wrong.

Rehearsed end to end on a local chain — `scripts/rehearse-upgrade.sh` reproduces
it. Everything below has been observed, not assumed.

---

## What happens

1. A `MsgSoftwareUpgrade` proposal names an upgrade and a **height**.
2. The proposal passes and the plan is stored.
3. Every node reaches that height and **halts**, logging:

   ```
   UPGRADE "<name>" NEEDED at height: <height>
   ```

4. Operators swap in a binary whose `Upgrades` list contains a matching entry.
5. The binary runs the handler once, migrates state, and continues.

A node running a binary **without** a matching entry halts again at the same
height. That is the intended behaviour and it is the thing to expect at 3am: the
chain is not broken, the binary is wrong.

---

## The mistake that costs a week

**The plan height must be beyond the end of the voting period, not beyond now.**

`x/upgrade` rejects a plan whose height has already passed by the time the
proposal executes. The proposal then shows `PROPOSAL_STATUS_FAILED` — it passed
the vote and failed to apply, which reads confusingly because "passed" and
"failed" are both true of it.

The voting period is **7 days**. At 5s blocks that is roughly **120,000 blocks**.
So a real proposal has to target a height at least that far out, plus margin for
block times drifting.

```
current height + (voting period ÷ block time) + margin
```

Get it wrong and you wait another 7 days to propose again.

This is not hypothetical: the first run of the rehearsal script failed exactly
this way, with a plan 25 blocks out and a voting period 40 blocks long.

---

## Writing the upgrade

Add an entry to `Upgrades` in `app/upgrades.go`:

```go
var Upgrades = []Upgrade{
    {Name: "v2", CreateHandler: defaultUpgradeHandler},
}
```

`defaultUpgradeHandler` runs the registered module migrations and nothing else,
which is right for an upgrade that changes logic or parameters but not the set of
modules.

**If the upgrade adds, renames or removes a module store**, declare it —
otherwise the node fails to start, because the commit multistore has to be told
about a new store key before it can load it:

```go
{
    Name: "v2",
    CreateHandler: defaultUpgradeHandler,
    StoreUpgrades: storetypes.StoreUpgrades{Added: []string{"newmodule"}},
}
```

Entries stay in the list after they have been applied. A node syncing from
genesis replays each in turn, so removing one breaks sync from scratch.

---

## Proposing it

```bash
CURRENT=$(earthd query block --type height 0 -o json | jq -r .header.height)
HEIGHT=$(( CURRENT + 130000 ))   # 7-day vote at 5s blocks, plus margin
GOV=$(earthd query auth module-account gov -o json | jq -r .account.value.address)
```

`plan.json`:

```json
{
  "messages": [
    {
      "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
      "authority": "GOV_ADDRESS",
      "plan": { "name": "v2", "height": "HEIGHT", "info": "release URL and sha256" }
    }
  ],
  "metadata": "",
  "deposit": "1000000uerth",
  "title": "Upgrade to v2",
  "summary": "What changes, and why. Link the release."
}
```

Put the release URL and the binary's sha256 in `info`. That field is what
cosmovisor reads to fetch the binary automatically, and what an operator reads to
check they have the right one.

```bash
earthd tx gov submit-proposal plan.json --from <key> \
  --chain-id earth-1 --gas auto --gas-adjustment 1.5 --gas-prices 0.005uerth
```

**Every transaction needs `--gas-prices`** at or above the node's minimum, or it
is rejected with `insufficient fee` before it reaches the mempool.

---

## On the day

Before the height:

- Publish the new binary and its checksum.
- Confirm operators have it staged.

At the height, each node halts. Then:

```bash
# swap the binary
sudo install earthd /usr/local/bin/earthd
earthd version --long          # confirm it is the right one
earthd start
```

The log shows `applying upgrade "<name>" at height` and blocks resume.

**With cosmovisor** the swap is automatic — stage the binary under
`$DAEMON_HOME/cosmovisor/upgrades/<name>/bin/earthd` in advance and it switches
at the halt without anyone awake.

---

## If it goes wrong

**Halts again after swapping** — the binary has no entry matching the plan name.
Check `earthd version` and the spelling of the name.

**Fails to start with a store error** — the upgrade adds a module store and the
entry has no `StoreUpgrades`.

**The upgrade was a mistake and has to be skipped.** Every node must agree, or
those that skip fork from those that do not:

```bash
earthd start --unsafe-skip-upgrades <height>
```

Coordinate it publicly and explicitly. A node that skips alone is on a different
chain from that block on.

**The chain is halted and consensus cannot resume.** That is a genesis restart,
not an upgrade: export state, build a new genesis from it, and start a new chain
id. `scripts/build-genesis.sh` and `networks/genesis/README.md` cover the mechanics.

---

## Rehearsing

```bash
scripts/rehearse-upgrade.sh
```

Starts a throwaway chain, passes a real proposal, waits for the halt, checks the
old binary refuses to continue, rebuilds with a handler, and checks the chain
resumes. It edits `app/upgrades.go` while it runs and restores it on exit,
including on failure.

Run it before any upgrade that matters.
