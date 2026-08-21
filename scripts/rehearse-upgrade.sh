#!/usr/bin/env bash
#
# Rehearses a governance upgrade end to end on a throwaway local chain.
#
# app/upgrades.go is well built and has never run. `var Upgrades = []Upgrade{}`
# means the halt, the store loader and the handler lookup have executed exactly
# zero times, and the first time they do will be a real upgrade on a live chain
# with validators waiting. This turns that into something already seen once.
#
# What it does:
#
#   1. starts a single-validator chain from deploy/genesis.json
#   2. passes a MsgSoftwareUpgrade proposal for a plan a few blocks out
#   3. waits for the chain to halt with UPGRADE "<name>" NEEDED
#   4. rebuilds earthd with a matching entry in Upgrades
#   5. restarts, and checks the chain continues past the halt height
#
# Step 5 is the one that matters. A binary WITHOUT the matching entry halts
# again at the same height, which is the failure mode operators hit at 3am.
#
#   scripts/rehearse-upgrade.sh            run it
#   scripts/rehearse-upgrade.sh --keep     leave the chain home behind
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="rehearsal-v2"
CHAIN_ID="earth-rehearsal"
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

WORK="$(mktemp -d)"
HOME_DIR="$WORK/node"
cleanup() {
  [ -n "${NODE_PID:-}" ] && kill "$NODE_PID" 2>/dev/null || true
  # Always put upgrades.go back: this script edits tracked source.
  [ -f "$WORK/upgrades.go.orig" ] && cp "$WORK/upgrades.go.orig" "$REPO/app/upgrades.go"
  if [ "$KEEP" = "1" ]; then echo "chain home kept at $HOME_DIR"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m   %s\n' "$*"; }
die()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; exit 1; }

E="$WORK/earthd"
KR="--keyring-backend test --home $HOME_DIR"
# The node enforces a 0.005uerth floor, so every tx here has to pay it. An
# operator hits the same wall; see docs/JOIN.md.
TXFLAGS="--chain-id $CHAIN_ID --node tcp://127.0.0.1:26657 --gas auto --gas-adjustment 1.6 --gas-prices 0.005uerth -y"
Q="--home $HOME_DIR --node tcp://127.0.0.1:26657"

step "build the 'old' binary (Upgrades is empty)"
cp "$REPO/app/upgrades.go" "$WORK/upgrades.go.orig"
( cd "$REPO" && go build -o "$E" ./cmd/earthd )
grep -q 'Upgrades = \[\]Upgrade{}' "$REPO/app/upgrades.go" \
  && ok "Upgrades is empty, as it is on master" \
  || die "Upgrades is not empty — this rehearsal assumes a clean starting point"

step "start a single-validator chain"
"$E" init rehearsal --chain-id "$CHAIN_ID" --home "$HOME_DIR" >/dev/null 2>&1
"$E" keys add val $KR >/dev/null 2>&1
VAL="$("$E" keys show val -a $KR)"
cp "$REPO/deploy/genesis.json" "$HOME_DIR/config/genesis.json"

# Rewrite what a rehearsal needs: its own chain id, a voting period measured in
# seconds rather than a week, and a genesis_time of now so nothing catches up.
python3 - "$HOME_DIR/config/genesis.json" "$CHAIN_ID" <<'PY'
import json, sys, datetime
p, chain_id = sys.argv[1], sys.argv[2]
g = json.load(open(p))
g['chain_id'] = chain_id
g['genesis_time'] = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
gov = g['app_state']['gov']['params']
gov['voting_period'] = '20s'
gov['expedited_voting_period'] = '10s'
gov['max_deposit_period'] = '60s'
json.dump(g, open(p, 'w'), indent=2)
PY

"$E" genesis add-genesis-account "$VAL" 200000000000uerth $KR >/dev/null
"$E" genesis gentx val 100000000000uerth --chain-id "$CHAIN_ID" $KR >/dev/null 2>&1
"$E" genesis collect-gentxs --home "$HOME_DIR" >/dev/null 2>&1

sed 's|^timeout_commit = .*|timeout_commit = "500ms"|' "$HOME_DIR/config/config.toml" > "$WORK/c" && mv "$WORK/c" "$HOME_DIR/config/config.toml"
sed 's|^minimum-gas-prices = .*|minimum-gas-prices = "0.005uerth"|' "$HOME_DIR/config/app.toml" > "$WORK/a" && mv "$WORK/a" "$HOME_DIR/config/app.toml"

"$E" start --home "$HOME_DIR" > "$WORK/node.log" 2>&1 &
NODE_PID=$!
for _ in $(seq 1 60); do
  H="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "${H:-}" ] && [ "$H" -gt 2 ] 2>/dev/null && break
  sleep 1
done
[ -n "${H:-}" ] || die "chain did not start — see $WORK/node.log"
ok "producing blocks (height $H)"

step "propose the upgrade"
GOV="$("$E" query auth module-account gov $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account"]["value"]["address"])')"
ok "gov authority is $GOV (a module account — nobody holds its key)"

# The plan height has to be beyond the END of the voting period, not beyond now.
# x/upgrade rejects a plan whose height is already past when the proposal
# executes, and the proposal then reads PROPOSAL_STATUS_FAILED — passed the vote,
# failed to apply. At 500ms blocks a 20s vote is ~40 blocks, so this leaves room.
#
# On mainnet the same rule bites much harder: a 7-day voting period is ~120,000
# blocks at 5s, so a real MsgSoftwareUpgrade has to target a height at least that
# far out. Getting it wrong costs another 7 days.
UPGRADE_HEIGHT=$(( H + 150 ))
cat > "$WORK/plan.json" <<JSON
{
  "messages": [
    {
      "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
      "authority": "$GOV",
      "plan": { "name": "$NAME", "height": "$UPGRADE_HEIGHT", "info": "rehearsal" }
    }
  ],
  "metadata": "",
  "deposit": "1000000uerth",
  "title": "Rehearsal upgrade $NAME",
  "summary": "No-op upgrade to exercise the halt-and-resume path."
}
JSON

TXOUT="$("$E" tx gov submit-proposal "$WORK/plan.json" --from val $KR $TXFLAGS 2>&1)" \
  || die "submit-proposal failed: $TXOUT"
TXHASH="$(printf '%s' "$TXOUT" | python3 -c 'import json,sys,re; t=sys.stdin.read(); m=re.search(r"\"txhash\":\s*\"([A-F0-9]+)\"",t); print(m.group(1) if m else "")' 2>/dev/null || true)"
sleep 5
if [ -n "$TXHASH" ]; then
  RES="$("$E" query tx "$TXHASH" $Q -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["code"], d.get("raw_log","")[:200])' 2>/dev/null || echo "?")"
  [ "${RES%% *}" = "0" ] || die "proposal tx failed: $RES"
fi
PID_NUM="$("$E" query gov proposals $Q -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["proposals"][-1]["id"])' 2>/dev/null || true)"
[ -n "$PID_NUM" ] || die "no proposal found after submit. tx said: $TXOUT"
ok "proposal $PID_NUM targets height $UPGRADE_HEIGHT"

"$E" tx gov vote "$PID_NUM" yes --from val $KR $TXFLAGS >/dev/null 2>&1
sleep 25
STATUS="$("$E" query gov proposal "$PID_NUM" $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["proposal"]["status"])')"
if [ "$STATUS" = "PROPOSAL_STATUS_FAILED" ]; then
  die "proposal passed the vote but failed to execute — almost always a plan
     height that had already gone by when the vote ended. Height is now
     $("$E" query block --type height 0 $Q -o json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["header"]["height"])' 2>/dev/null || echo '?'), plan targeted $UPGRADE_HEIGHT."
fi
[ "$STATUS" = "PROPOSAL_STATUS_PASSED" ] || die "proposal did not pass: $STATUS"
ok "passed"

step "wait for the halt"
for _ in $(seq 1 90); do
  grep -q "UPGRADE \"$NAME\" NEEDED" "$WORK/node.log" && break
  sleep 1
done
grep -q "UPGRADE \"$NAME\" NEEDED" "$WORK/node.log" \
  || die "chain never halted — see $WORK/node.log"
ok "halted with UPGRADE \"$NAME\" NEEDED at height $UPGRADE_HEIGHT"
kill "$NODE_PID" 2>/dev/null || true; wait "$NODE_PID" 2>/dev/null || true; NODE_PID=""

step "restart on the OLD binary — must halt again"
"$E" start --home "$HOME_DIR" > "$WORK/old-again.log" 2>&1 &
OLD_PID=$!
sleep 8
kill "$OLD_PID" 2>/dev/null || true; wait "$OLD_PID" 2>/dev/null || true
grep -q "UPGRADE \"$NAME\" NEEDED\|BINARY UPDATED BEFORE TRIGGER" "$WORK/old-again.log" \
  && ok "old binary refuses to continue, as it should" \
  || die "old binary continued past the upgrade height — that is a consensus break"

step "build the NEW binary with a matching handler"
python3 - "$REPO/app/upgrades.go" "$NAME" <<'PY'
import sys
p, name = sys.argv[1], sys.argv[2]
s = open(p).read()
s = s.replace(
    'var Upgrades = []Upgrade{}',
    'var Upgrades = []Upgrade{\n\t{Name: "%s", CreateHandler: defaultUpgradeHandler},\n}' % name)
open(p, 'w').write(s)
PY
( cd "$REPO" && go build -o "$E" ./cmd/earthd )
ok "rebuilt with Upgrades containing $NAME"

step "restart on the new binary — must continue"
"$E" start --home "$HOME_DIR" > "$WORK/new.log" 2>&1 &
NODE_PID=$!
FINAL=""
for _ in $(seq 1 60); do
  FINAL="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null && break
  sleep 1
done
[ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null \
  || die "chain did not pass the upgrade height — see $WORK/new.log"
ok "applied the upgrade and continued (height $FINAL > $UPGRADE_HEIGHT)"

grep -q "applying upgrade \"$NAME\"" "$WORK/new.log" \
  && ok "handler ran" \
  || printf '  note: no "applying upgrade" line; check %s\n' "$WORK/new.log"

printf '\n\033[32mrehearsal complete\033[0m — halt, refusal, and resume all behave\n'
