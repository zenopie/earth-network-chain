#!/usr/bin/env bash
#
# Rehearses the upgrade path cosmovisor takes when it arrives AFTER the halt —
# the one earth-1 actually failed on, and the one scripts/rehearse-cosmovisor.sh
# does not cover.
#
# That script starts cosmovisor first, so its file watcher notices
# data/upgrade-info.json while earthd is still alive. cosmovisor then asks the
# running node its height, gets an answer, and swaps. Every released cosmovisor
# passes it. It is the happy path, and it is not what a small node does.
#
# On a slow node the process is gone before that question can be asked, and
# cosmovisor's only remaining move is the recheck it performs after the child
# exits:
#
#     case err := <-cmdDone:
#         l.fw.Stop()
#         // the app x/upgrade causes a panic and the app can die before the
#         // filewatcher finds the update, so we need to recheck update-info file.
#         if !l.fw.CheckUpdate(currentUpgrade) { return false, err }
#
# CheckUpdate verifies the height by running `earthd status` against a node that
# has already crashed, so it can only error — and scanner.go reads an error as
# "not there yet". cosmovisor exits, the supervisor restarts it, and the chain
# replays into the same wall forever. earth-1 sat in exactly that loop at height
# 30100 on 2026-08-30 until the binary was staged by hand.
#
# So this rehearsal reproduces the real sequence:
#
#   1. run the OLD binary directly, with no supervisor at all
#   2. pass an upgrade it has no handler for
#   3. let it halt and EXIT — nothing is running, upgrade-info.json is on disk
#   4. only then start cosmovisor, against an already-halted home
#   5. check whether it recovers the chain unattended
#
#   scripts/rehearse-cosmovisor-restart.sh --expect-deadlock  today: reproduces
#   scripts/rehearse-cosmovisor-restart.sh                    when it is fixed
#   COSMOVISOR_VERSION=<sha> scripts/...                      try another build
#
# --expect-deadlock inverts the verdict, so today's outcome is a green
# "reproduced" rather than a red failure. Drop the flag to check whether a build
# has finally fixed it.
#
# MEASURED 2026-08-30, both deadlock here:
#
#   v1.7.1                                     the released build in the Dockerfile
#   b3342d9962838fd9e4452cf155c50529c4e185f0   cosmos-sdk #23720, "get block
#                                              height from db after node
#                                              execution fails"
#
# #23720 looks like the fix and is not one. When the process is stopped it reads
# the height from the blockstore instead of the RPC — but it picks the backend
# like this:
#
#     result, _ := exec.Command(bin, "config", "get", "config", "db_backend", ...).CombinedOutput()
#     blockStoreDB, err := dbm.NewDB("blockstore", dbm.BackendType(result), ...)
#
# and `earthd config get config db_backend` prints `"goleveldb"\n` — quoted, with
# a newline, which is the SDK's own output format. That is not a registered
# backend, dbm.NewDB errors, and CheckUpdate reads the error as "not there yet",
# which is the same dead end by a different road. CombinedOutput also folds in
# stderr, so any warning a binary prints would break it too.
#
# So there is no cosmovisor build that can currently recover a node that is
# already down, and the entrypoint stages the binary instead. Run this without
# --expect-deadlock whenever a new cosmovisor appears; the day it passes, the
# staging in docker/entrypoint.sh can go.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="cvrestart-rehearsal"
CHAIN_ID="earth-cvrestart"
# Default matches the Dockerfile's pin. Override to compare builds.
COSMOVISOR_VERSION="${COSMOVISOR_VERSION:-v1.7.1}"
EXPECT_DEADLOCK=0
KEEP=0
for a in "$@"; do
  case "$a" in
    --expect-deadlock) EXPECT_DEADLOCK=1 ;;
    --keep) KEEP=1 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

WORK="$(mktemp -d)"
HOME_DIR="$WORK/node"
SERVE="$WORK/serve"
mkdir -p "$SERVE"

cleanup() {
  [ -n "${CV_PID:-}" ] && kill "$CV_PID" 2>/dev/null || true
  [ -n "${NODE_PID:-}" ] && kill "$NODE_PID" 2>/dev/null || true
  # Killing cosmovisor does not kill the earthd it spawned; match on the temp
  # home so this can never touch a node the operator is running deliberately.
  pkill -f "$HOME_DIR" 2>/dev/null || true
  [ -n "${HTTP_PID:-}" ] && kill "$HTTP_PID" 2>/dev/null || true
  [ -f "$WORK/upgrades.go.orig" ] && cp "$WORK/upgrades.go.orig" "$REPO/app/upgrades.go"
  if [ "$KEEP" = "1" ]; then echo "chain home kept at $HOME_DIR"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '  \033[32mok\033[0m   %s\n' "$*"; }
die()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; exit 1; }

OLD="$WORK/earthd-old"
NEW="$WORK/earthd-new"
KR="--keyring-backend test --home $HOME_DIR"
TXFLAGS="--chain-id $CHAIN_ID --node tcp://127.0.0.1:26657 --gas auto --gas-adjustment 1.6 --gas-prices 0.005uerth -y"
Q="--home $HOME_DIR --node tcp://127.0.0.1:26657"

if curl -s --max-time 2 localhost:26657/status >/dev/null 2>&1; then
  die "something is already listening on 26657 — a leftover node would answer
     this rehearsal's queries and make the result meaningless. Find it with
     'lsof -nP -iTCP:26657 -sTCP:LISTEN' and stop it first."
fi

step "get cosmovisor ($COSMOVISOR_VERSION)"
# Always build the version under test. Deliberately NOT `command -v cosmovisor`:
# the whole point here is which build is running, so a copy on PATH would quietly
# invalidate the result.
GOBIN="$WORK/bin" go install "cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@$COSMOVISOR_VERSION"
CV="$WORK/bin/cosmovisor"
ok "built $("$CV" version 2>&1 | head -1 | sed 's/^/  /' | xargs)"

step "build the 'old' binary (no handler for $NAME)"
cp "$REPO/app/upgrades.go" "$WORK/upgrades.go.orig"
grep -q "Name:[[:space:]]*\"$NAME\"" "$REPO/app/upgrades.go" \
  && die "Upgrades already contains $NAME — pick a name this binary does not handle"
( cd "$REPO" && go build -o "$OLD" ./cmd/earthd )
ok "built, and it has no handler for $NAME"

step "build the 'new' binary and package it the way a release is packaged"
python3 - "$REPO/app/upgrades.go" "$NAME" <<'PY'
import sys
p, name = sys.argv[1], sys.argv[2]
s = open(p).read()
entry = '\t{Name: "%s", CreateHandler: defaultUpgradeHandler},\n' % name
marker = 'var Upgrades = []Upgrade{'
i = s.index(marker) + len(marker)
s = s[:i] + '\n' + entry + s[i:].lstrip('\n')
open(p, 'w').write(s)
PY
( cd "$REPO" && go build -o "$NEW" ./cmd/earthd )
cp "$WORK/upgrades.go.orig" "$REPO/app/upgrades.go"
ok "built with a handler for $NAME, and upgrades.go restored"

mkdir -p "$SERVE/pkg/bin"
cp "$NEW" "$SERVE/pkg/bin/earthd"
tar -C "$SERVE/pkg" -czf "$SERVE/upgrade.tar.gz" bin
SUM="$(shasum -a 256 "$SERVE/upgrade.tar.gz" | awk '{print $1}')"
ok "sha256 $SUM"

step "serve it over loopback"
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
( cd "$SERVE" && python3 -m http.server "$PORT" --bind 127.0.0.1 >"$WORK/http.log" 2>&1 ) &
HTTP_PID=$!
disown "$HTTP_PID" 2>/dev/null || true
for _ in $(seq 1 30); do
  curl -fsS --max-time 1 "http://127.0.0.1:$PORT/upgrade.tar.gz" -o /dev/null 2>/dev/null && break
  sleep 0.3
done
curl -fsS --max-time 2 "http://127.0.0.1:$PORT/upgrade.tar.gz" -o /dev/null \
  || die "local http server never came up — see $WORK/http.log"
URL="http://127.0.0.1:$PORT/upgrade.tar.gz?checksum=sha256:$SUM"
ok "serving on 127.0.0.1:$PORT"

step "lay out the chain and the cosmovisor directories"
"$OLD" init cvrestart --chain-id "$CHAIN_ID" --home "$HOME_DIR" >/dev/null 2>&1
"$OLD" keys add val $KR >/dev/null 2>&1
VAL="$("$OLD" keys show val -a $KR)"
cp "$REPO/networks/genesis.json" "$HOME_DIR/config/genesis.json"

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

"$OLD" genesis add-genesis-account "$VAL" 200000000000uerth $KR >/dev/null
"$OLD" genesis gentx val 100000000000uerth --chain-id "$CHAIN_ID" $KR >/dev/null 2>&1
"$OLD" genesis collect-gentxs --home "$HOME_DIR" >/dev/null 2>&1

sed 's|^timeout_commit = .*|timeout_commit = "500ms"|' "$HOME_DIR/config/config.toml" > "$WORK/c" && mv "$WORK/c" "$HOME_DIR/config/config.toml"
sed 's|^minimum-gas-prices = .*|minimum-gas-prices = "0.005uerth"|' "$HOME_DIR/config/app.toml" > "$WORK/a" && mv "$WORK/a" "$HOME_DIR/config/app.toml"

mkdir -p "$HOME_DIR/cosmovisor/genesis/bin"
cp "$OLD" "$HOME_DIR/cosmovisor/genesis/bin/earthd"
ok "cosmovisor/genesis/bin/earthd is the old binary"

step "start the chain with NO supervisor — this is the difference"
"$OLD" start --home "$HOME_DIR" > "$WORK/node.log" 2>&1 &
NODE_PID=$!
H=""
for _ in $(seq 1 90); do
  H="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "${H:-}" ] && [ "$H" -gt 2 ] 2>/dev/null && break
  sleep 1
done
[ -n "${H:-}" ] || die "chain did not start — see $WORK/node.log"
ok "producing blocks unsupervised (height $H)"

step "propose the upgrade, with the download URL in info"
GOV="$("$OLD" query auth module-account gov $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account"]["value"]["address"])')"
# H+150, not something tighter: the vote alone takes ~30s and blocks are 500ms,
# so a small margin means the plan height has already passed by the time the
# proposal executes — x/upgrade then rejects the plan and nothing ever halts.
UPGRADE_HEIGHT=$(( H + 150 ))
OSARCH="$(go env GOOS)/$(go env GOARCH)"
python3 - "$WORK/plan.json" "$GOV" "$NAME" "$UPGRADE_HEIGHT" "$OSARCH" "$URL" <<'PY'
import json, sys
out, gov, name, height, osarch, url = sys.argv[1:7]
json.dump({
    "messages": [{
        "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
        "authority": gov,
        "plan": {"name": name, "height": str(height),
                 "info": json.dumps({"binaries": {osarch: url}})},
    }],
    "metadata": "", "deposit": "1000000uerth",
    "title": "Cosmovisor halted-restart rehearsal %s" % name,
    "summary": "Halts unsupervised, then hands an already-halted home to cosmovisor.",
}, open(out, "w"), indent=2)
PY
TXOUT="$("$OLD" tx gov submit-proposal "$WORK/plan.json" --from val $KR $TXFLAGS 2>&1)" \
  || die "submit-proposal failed: $TXOUT"
sleep 5
PID_NUM="$("$OLD" query gov proposals $Q -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["proposals"][-1]["id"])' 2>/dev/null || true)"
[ -n "$PID_NUM" ] || die "no proposal found after submit. tx said: $TXOUT"
"$OLD" tx gov vote "$PID_NUM" yes --from val $KR $TXFLAGS >/dev/null 2>&1
sleep 25
STATUS="$("$OLD" query gov proposal "$PID_NUM" $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["proposal"]["status"])')"
[ "$STATUS" = "PROPOSAL_STATUS_PASSED" ] || die "proposal did not pass: $STATUS"
ok "proposal $PID_NUM passed, targeting height $UPGRADE_HEIGHT"

step "let it halt, then end it the way a container restart does"
for _ in $(seq 1 180); do
  grep -q "UPGRADE \"$NAME\" NEEDED" "$WORK/node.log" && break
  sleep 1
done
grep -q "UPGRADE \"$NAME\" NEEDED" "$WORK/node.log" \
  || die "node never halted at the plan height — see $WORK/node.log"
ok "halted at $UPGRADE_HEIGHT"

# Do NOT wait for the process to exit: it often will not. The halt surfaces as a
# CONSENSUS FAILURE, the app closes application.db and the RPC stops answering,
# and then earthd can simply sit there. Under cosmovisor the parent notices the
# child is done; run bare, as here, nothing does. In production the container's
# restart is what ends it, so end it the same way — and make sure it is really
# gone, because a survivor still holds the database that cosmovisor's earthd is
# about to open.
kill "$NODE_PID" 2>/dev/null || true
for _ in $(seq 1 30); do kill -0 "$NODE_PID" 2>/dev/null || break; sleep 1; done
kill -9 "$NODE_PID" 2>/dev/null || true
wait "$NODE_PID" 2>/dev/null || true
kill -0 "$NODE_PID" 2>/dev/null && die "could not end the halted node (pid $NODE_PID)"
NODE_PID=""
ok "process ended — nothing holds the database"
[ -f "$HOME_DIR/data/upgrade-info.json" ] \
  || die "no data/upgrade-info.json — the app did not record the plan"
ok "data/upgrade-info.json on disk: $(python3 -c 'import json;d=json.load(open("'"$HOME_DIR"'/data/upgrade-info.json"));print(d["name"],"@",d["height"])')"
curl -s --max-time 2 localhost:26657/status >/dev/null 2>&1 \
  && die "something still answers on 26657 — the halt did not free the node"
ok "nothing is listening: this is the state earth-1 restart-loops in"

step "NOW start cosmovisor, against an already-halted home"
export DAEMON_NAME=earthd
export DAEMON_HOME="$HOME_DIR"
export DAEMON_RESTART_AFTER_UPGRADE=true
export DAEMON_ALLOW_DOWNLOAD_BINARIES=true
export DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true
export UNSAFE_SKIP_BACKUP=true          # rehearsal only; keep backups on a real node
"$CV" run start --home "$HOME_DIR" > "$WORK/cv.log" 2>&1 &
CV_PID=$!
FINAL=""
for _ in $(seq 1 150); do
  FINAL="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null && break
  # cosmovisor giving up and exiting is itself an outcome worth waiting for
  kill -0 "$CV_PID" 2>/dev/null || { sleep 2; break; }
  sleep 1
done

RECOVERED=0
if [ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null; then RECOVERED=1; fi

step "verdict"
if [ "$EXPECT_DEADLOCK" = "1" ]; then
  [ "$RECOVERED" = "1" ] && die "expected the deadlock, but cosmovisor recovered the chain.
     If this is a released build, the bug is fixed and the pin can go."
  ok "cosmovisor did not recover the chain — deadlock reproduced"
  grep -qi "no upgrade binary found, beginning to download\|downloading binary complete" "$WORK/cv.log" \
    && die "it downloaded after all — that is not the deadlock this reproduces"
  ok "no download was attempted, which is the signature: the height check errored"
  printf '\n\033[32mreproduced\033[0m — %s cannot upgrade a node that is already down\n' "$COSMOVISOR_VERSION"
  exit 0
fi

[ "$RECOVERED" = "1" ] || die "cosmovisor did not recover the chain from a halted start.
     This is the earth-1 deadlock. Last lines of cosmovisor's log:
$(tail -5 "$WORK/cv.log" | sed 's/^/       /')"
ok "resumed and passed the halt height ($FINAL > $UPGRADE_HEIGHT)"

grep -qi "no upgrade binary found, beginning to download\|downloading binary complete" "$WORK/cv.log" \
  && ok "cosmovisor downloaded the binary itself" \
  || die "chain advanced but nothing was downloaded — check $WORK/cv.log"

DL_SUM="$(shasum -a 256 "$HOME_DIR/cosmovisor/upgrades/$NAME/bin/earthd" | awk '{print $1}')"
[ "$DL_SUM" = "$(shasum -a 256 "$NEW" | awk '{print $1}')" ] \
  || die "downloaded binary is not the one that was served"
ok "downloaded binary matches what was served"

CUR="$(readlink "$HOME_DIR/cosmovisor/current" 2>/dev/null || echo '')"
case "$CUR" in
  *upgrades/$NAME*) ok "current -> $CUR" ;;
  *) die "current symlink still points at $CUR" ;;
esac

grep -q "applying upgrade \"$NAME\"" "$WORK/cv.log" \
  && ok "handler ran" \
  || printf '  note: no "applying upgrade" line; check %s\n' "$WORK/cv.log"

printf '\n\033[32mrehearsal complete\033[0m — cosmovisor recovered a chain that was already halted and down\n'
