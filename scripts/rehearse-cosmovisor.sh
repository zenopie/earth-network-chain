#!/usr/bin/env bash
#
# Rehearses an upgrade the way cosmovisor performs it: unattended, with the
# binary fetched from the URL in the governance proposal.
#
# scripts/rehearse-upgrade.sh already covers the manual path — halt, swap by
# hand, resume. This covers the half that path never touches, and the half with
# the most ways to fail silently:
#
#   * `info` is parsed as a JSON binaries map keyed by GOOS/GOARCH. Prose in
#     that field, or a missing key for the node's platform, fails the download.
#   * the archive must unpack to /<daemon> or /bin/<daemon> and nothing else.
#     A wrapper directory — earthd_v0.4.5_linux_amd64/bin/earthd, which is what
#     releases up to v0.4.5 shipped — matches neither.
#   * DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true rejects a plan with no ?checksum=.
#
# Every one of those fails at the upgrade height, with the chain already halted,
# which is the worst moment to discover any of them.
#
# The new binary is served over loopback rather than fetched from GitHub, so the
# rehearsal needs no release to exist and works offline. The archive is built in
# the same shape the release workflow produces.
#
# What it does:
#
#   1. builds an "old" binary with no handler for the plan name
#   2. builds a "new" binary that has one, packages it as bin/<daemon> at the
#      archive root, and serves it over 127.0.0.1
#   3. starts the chain under cosmovisor, pointed at the old binary
#   4. passes a MsgSoftwareUpgrade whose info names the served URL and its sha256
#   5. leaves it alone, and checks cosmovisor halted, downloaded, verified,
#      swapped and resumed — with nobody intervening
#
# Step 5 is the point. Nothing here is done by hand after the proposal passes.
#
#   scripts/rehearse-cosmovisor.sh            run it
#   scripts/rehearse-cosmovisor.sh --keep     leave the chain home behind
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="cosmovisor-rehearsal"
CHAIN_ID="earth-cvrehearsal"
COSMOVISOR_VERSION="v1.7.1"
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

WORK="$(mktemp -d)"
HOME_DIR="$WORK/node"
SERVE="$WORK/serve"
cleanup() {
  [ -n "${CV_PID:-}" ] && kill "$CV_PID" 2>/dev/null || true
  # Killing cosmovisor does NOT kill the earthd it spawned. The child keeps
  # running, keeps port 26657, and the next run of this script then talks to the
  # previous run's chain and fails with "account not found" — which reads like a
  # genesis bug and is not one. Match on the temp home, which is unique to this
  # run, so this cannot touch a node the operator is running deliberately.
  pkill -f "$HOME_DIR" 2>/dev/null || true
  [ -n "${HTTP_PID:-}" ] && kill "$HTTP_PID" 2>/dev/null || true
  # Always put upgrades.go back: this script edits tracked source.
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

# A stray node from an earlier run would answer every query in this script and
# silently invalidate the whole rehearsal. Fail here, clearly, instead.
if curl -s --max-time 2 localhost:26657/status >/dev/null 2>&1; then
  die "something is already listening on 26657 — a leftover node would answer
     this rehearsal's queries and make the result meaningless. Find it with
     'lsof -nP -iTCP:26657 -sTCP:LISTEN' and stop it first."
fi

step "get cosmovisor"
CV="$(command -v cosmovisor || true)"
if [ -z "$CV" ]; then
  GOBIN="$WORK/bin" go install "cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@$COSMOVISOR_VERSION"
  CV="$WORK/bin/cosmovisor"
fi
ok "cosmovisor at $CV"

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
cp "$WORK/upgrades.go.orig" "$REPO/app/upgrades.go"   # restore immediately
ok "built with a handler for $NAME, and upgrades.go restored"

# bin/ at the archive root, no wrapper directory — the shape .github/workflows/
# release.yml produces and the only one cosmovisor's unpacker accepts.
mkdir -p "$SERVE/pkg/bin"
cp "$NEW" "$SERVE/pkg/bin/earthd"
tar -C "$SERVE/pkg" -czf "$SERVE/upgrade.tar.gz" bin
SUM="$(shasum -a 256 "$SERVE/upgrade.tar.gz" | awk '{print $1}')"
ok "archive root: $(tar -tzf "$SERVE/upgrade.tar.gz" | tr '\n' ' ')"
ok "sha256 $SUM"

step "serve it over loopback"
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
( cd "$SERVE" && python3 -m http.server "$PORT" --bind 127.0.0.1 >"$WORK/http.log" 2>&1 ) &
HTTP_PID=$!
# Detach it from job control, or bash prints "Terminated: 15" after the success
# line when the trap kills it — which looks like the rehearsal failed at the end.
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
"$OLD" init cvrehearsal --chain-id "$CHAIN_ID" --home "$HOME_DIR" >/dev/null 2>&1
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

step "start the chain under cosmovisor"
export DAEMON_NAME=earthd
export DAEMON_HOME="$HOME_DIR"
export DAEMON_RESTART_AFTER_UPGRADE=true
export DAEMON_ALLOW_DOWNLOAD_BINARIES=true
export DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true
export UNSAFE_SKIP_BACKUP=true          # rehearsal only; keep backups on a real node
"$CV" run start --home "$HOME_DIR" > "$WORK/cv.log" 2>&1 &
CV_PID=$!
H=""
for _ in $(seq 1 90); do
  H="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "${H:-}" ] && [ "$H" -gt 2 ] 2>/dev/null && break
  sleep 1
done
[ -n "${H:-}" ] || die "chain did not start under cosmovisor — see $WORK/cv.log"
ok "producing blocks under cosmovisor (height $H)"

step "propose the upgrade, with the download URL in info"
GOV="$("$OLD" query auth module-account gov $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account"]["value"]["address"])')"
UPGRADE_HEIGHT=$(( H + 150 ))

# info is JSON, keyed by GOOS/GOARCH. Prose here is the single most common way a
# plan is accepted by governance and then fails every node at the halt.
OSARCH="$(go env GOOS)/$(go env GOARCH)"
python3 - "$WORK/plan.json" "$GOV" "$NAME" "$UPGRADE_HEIGHT" "$OSARCH" "$URL" <<'PY'
import json, sys
out, gov, name, height, osarch, url = sys.argv[1:7]
plan = {
    "messages": [{
        "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
        "authority": gov,
        "plan": {
            "name": name,
            "height": str(height),
            "info": json.dumps({"binaries": {osarch: url}}),
        },
    }],
    "metadata": "",
    "deposit": "1000000uerth",
    "title": "Cosmovisor rehearsal %s" % name,
    "summary": "Exercises the download, checksum and swap path end to end.",
}
json.dump(plan, open(out, "w"), indent=2)
PY
ok "info names $OSARCH and carries a sha256"

TXOUT="$("$OLD" tx gov submit-proposal "$WORK/plan.json" --from val $KR $TXFLAGS 2>&1)" \
  || die "submit-proposal failed: $TXOUT"
sleep 5
PID_NUM="$("$OLD" query gov proposals $Q -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["proposals"][-1]["id"])' 2>/dev/null || true)"
[ -n "$PID_NUM" ] || die "no proposal found after submit. tx said: $TXOUT"
"$OLD" tx gov vote "$PID_NUM" yes --from val $KR $TXFLAGS >/dev/null 2>&1
sleep 25
STATUS="$("$OLD" query gov proposal "$PID_NUM" $Q -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["proposal"]["status"])')"
[ "$STATUS" = "PROPOSAL_STATUS_PASSED" ] || die "proposal did not pass: $STATUS"
ok "proposal $PID_NUM passed, targeting height $UPGRADE_HEIGHT"

step "hands off — cosmovisor should do the rest"
FINAL=""
for _ in $(seq 1 180); do
  FINAL="$(curl -s --max-time 2 localhost:26657/status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null || true)"
  [ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null && break
  sleep 1
done

grep -q "UPGRADE \"$NAME\" NEEDED" "$WORK/cv.log" \
  && ok "chain halted at the plan height" \
  || die "chain never halted — see $WORK/cv.log"

grep -qi "no upgrade binary found, beginning to download\|downloading binary complete" "$WORK/cv.log" \
  && ok "cosmovisor downloaded the binary itself" \
  || die "cosmovisor did not download — see $WORK/cv.log"

[ -x "$HOME_DIR/cosmovisor/upgrades/$NAME/bin/earthd" ] \
  && ok "unpacked to cosmovisor/upgrades/$NAME/bin/earthd" \
  || die "downloaded archive did not land at upgrades/$NAME/bin/earthd — wrong archive shape"

# Prove the running binary is the downloaded one, not the old one carrying on.
DL_SUM="$(shasum -a 256 "$HOME_DIR/cosmovisor/upgrades/$NAME/bin/earthd" | awk '{print $1}')"
NEW_SUM="$(shasum -a 256 "$NEW" | awk '{print $1}')"
OLD_SUM="$(shasum -a 256 "$OLD" | awk '{print $1}')"
[ "$DL_SUM" = "$NEW_SUM" ] || die "downloaded binary is not the one that was served"
[ "$DL_SUM" != "$OLD_SUM" ] || die "downloaded binary is identical to the old one — nothing was swapped"
ok "downloaded binary matches what was served, and differs from the old one"

CUR="$(readlink "$HOME_DIR/cosmovisor/current" 2>/dev/null || echo '')"
case "$CUR" in
  *upgrades/$NAME*) ok "current -> $(basename "$(dirname "$CUR")")/$(basename "$CUR")" ;;
  *) die "current symlink still points at $CUR" ;;
esac

[ -n "$FINAL" ] && [ "$FINAL" -gt "$UPGRADE_HEIGHT" ] 2>/dev/null \
  || die "chain did not pass the upgrade height — see $WORK/cv.log"
ok "resumed and passed the halt height ($FINAL > $UPGRADE_HEIGHT)"

grep -q "applying upgrade \"$NAME\"" "$WORK/cv.log" \
  && ok "handler ran" \
  || printf '  note: no "applying upgrade" line; check %s\n' "$WORK/cv.log"

printf '\n\033[32mrehearsal complete\033[0m — cosmovisor halted, downloaded, verified, swapped and resumed unattended\n'
