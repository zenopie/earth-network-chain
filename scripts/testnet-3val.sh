#!/usr/bin/env bash
#
# Brings up a local 3-validator network and leaves it running.
#
#   ./scripts/testnet-3val.sh up      # build genesis, start 3 nodes, promote 2
#   ./scripts/testnet-3val.sh down    # stop everything and clean up
#
# Node 0 is the genesis validator, built from networks/genesis.json — the same
# file earth-1 launched with, so it carries the real CSCAs, verifying keys and
# ANML/ERTH pool rather than an approximation of them. Nodes 1 and 2 sync, then
# join the validator set with MsgCreateValidator — the same path a real operator
# takes, which also exercises validator-set changes at runtime rather than only
# at genesis.
#
# This used to shell out to `ignite chain init`. ignite is no longer part of
# building this chain — `make proto-gen` calls buf directly — and this script was
# the last thing dragging it back in, so the local testnet stopped working the
# moment ignite's own toolchain handling broke (it tries to fetch a pinned Go
# version and fails if it is not there). Building genesis from the file we
# actually ship removes the dependency and tests something closer to the real
# chain.
#
# Voting power is deliberately uneven (see STAKES): it makes the >2/3 liveness
# threshold observable — losing the largest validator halts the chain, losing a
# smaller one does not.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EARTHD="${EARTHD:-$HOME/go/bin/earthd}"
# Deliberately NOT earth-1, even though the genesis it is built from carries that
# id. This is a throwaway local network, and sharing the mainnet chain id would
# make a transaction signed here replayable there.
#
# It is set once, here, and every transaction below is signed for it. This used
# to be assumed rather than set, and was wrong: every tx came back "signature
# verification failed", the script discarded the output, nodes 1 and 2 silently
# never joined the validator set, and `up` reported success anyway.
CHAIN_ID="${CHAIN_ID:-earth-3val}"
BASE=/tmp/earth-3val
GENESIS_SRC="$CHAIN_DIR/networks/genesis.json"
# Optional: shorten governance so a proposal can be driven end to end by hand.
# Unset keeps what the launch genesis carries, which is a seven-day vote.
GOV_VOTING_PERIOD="${GOV_VOTING_PERIOD:-}"

# node index -> RPC / P2P / API / gRPC ports
RPC=(26657 26667 26677)
P2P=(26656 26666 26676)
API=(1317 1327 1337)
GRPC=(9090 9190 9290)
# uerth each joining validator self-delegates; node 0's stake comes from genesis.
STAKES=(0 300000000 50000000)

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# tx runs a transaction and fails the script if the chain rejected it.
#
# These used to end in `>/dev/null`. A transaction can be delivered and still
# fail — CheckTx reports a non-zero `code` in a JSON body on stdout with exit
# status 0 — so discarding the output discards the only report of what happened.
# That is how a wrong chain id became a script that finished happily with one
# validator instead of three.
tx() {
  local out code
  out="$("$@" -y -o json 2>&1)" || {
    echo "error: transaction could not be sent:" >&2; echo "$out" >&2; exit 1; }
  code="$(printf '%s' "$out" | python3 -c "import json,sys
try:
    print(json.loads(sys.stdin.read().strip().splitlines()[-1])['code'])
except Exception:
    print('unparseable')" 2>/dev/null)"
  [ "$code" = "0" ] || {
    echo "error: transaction rejected (code $code):" >&2; echo "$out" >&2; exit 1; }
}

# wait_blocks waits for node0 to advance N blocks.
#
# This replaces a `sleep 4` between transactions. A fixed sleep is a bet on the
# block time, and it lost the moment genesis stopped coming from ignite: the
# launch genesis leaves CometBFT's 5s timeout_commit alone, 4s is less than one
# block, and the second transaction from the same key was therefore built
# against a sequence the first had not yet consumed. It failed with "account
# sequence mismatch, expected 2, got 1" and took the whole script down with it.
wait_blocks() {
  local want="$1" start now
  start=$(node0_height)
  for _ in $(seq 120); do
    now=$(node0_height)
    [ "${now:-0}" -ge $(( ${start:-0} + want )) ] && return 0
    sleep 1
  done
  echo "error: node0 stopped producing blocks" >&2; exit 1
}

node0_height() {
  curl -s "http://127.0.0.1:${RPC[0]}/status" 2>/dev/null \
    | python3 -c "import json,sys;print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null \
    || echo 0
}

up() {
  [ -x "$EARTHD" ] || { echo "error: no earthd at $EARTHD" >&2; exit 1; }
  [ -f "$GENESIS_SRC" ] || { echo "error: no genesis at $GENESIS_SRC" >&2; exit 1; }

  rm -rf "$BASE"; mkdir -p "$BASE"

  say "node0: building genesis from networks/genesis.json (chain id $CHAIN_ID)"
  "$EARTHD" init node0 --chain-id "$CHAIN_ID" --home "$BASE/n0" >/dev/null 2>&1
  "$EARTHD" keys add alice --keyring-backend test --home "$BASE/n0" >/dev/null 2>&1
  cp "$GENESIS_SRC" "$BASE/n0/config/genesis.json"

  # Rewrite only what a local network cannot inherit: its own chain id, a
  # genesis_time of now so it does not spend its first minutes catching up, and
  # the gentx list, which collect-gentxs replaces below.
  python3 "$CHAIN_DIR/scripts/lib/localize-genesis.py" \
    "$BASE/n0/config/genesis.json" "$CHAIN_ID" "$GOV_VOTING_PERIOD"

  alice_addr=$("$EARTHD" keys show alice -a --keyring-backend test --home "$BASE/n0")
  "$EARTHD" genesis add-genesis-account "$alice_addr" 500000000000uerth \
    --keyring-backend test --home "$BASE/n0" >/dev/null
  "$EARTHD" genesis gentx alice 100000000000uerth --chain-id "$CHAIN_ID" \
    --keyring-backend test --home "$BASE/n0" >/dev/null 2>&1
  "$EARTHD" genesis collect-gentxs --home "$BASE/n0" >/dev/null 2>&1
  say "node0: genesis ready"

  node0_id=$("$EARTHD" comet show-node-id --home "$BASE/n0")

  for i in 1 2; do
    say "node$i: init + copy genesis"
    "$EARTHD" init "node$i" --chain-id "$CHAIN_ID" --home "$BASE/n$i" >/dev/null 2>&1
    cp "$BASE/n0/config/genesis.json" "$BASE/n$i/config/genesis.json"
  done

  # All three nodes share 127.0.0.1. CometBFT drops additional peers from an IP
  # it already has a connection to, and treats loopback as unroutable for the
  # address book — so without these only the first joiner ever connects.
  for i in 0 1 2; do
    cfg="$BASE/n$i/config/config.toml"
    perl -0777 -pi -e 's|^allow_duplicate_ip = false|allow_duplicate_ip = true|m' "$cfg"
    perl -0777 -pi -e 's|^addr_book_strict = true|addr_book_strict = false|m' "$cfg"
    # A local network has no reason to take five seconds a block.
    perl -0777 -pi -e 's|^timeout_commit = .*|timeout_commit = "1s"|m' "$cfg"
  done

  for i in 0 1 2; do
    peers=""
    [ "$i" -ne 0 ] && peers="--p2p.persistent_peers $node0_id@127.0.0.1:${P2P[0]}"
    say "node$i: starting (rpc ${RPC[$i]}, p2p ${P2P[$i]}, api ${API[$i]})"
    ( cd /tmp && nohup "$EARTHD" start --home "$BASE/n$i" \
        --minimum-gas-prices 0uerth \
        --rpc.laddr "tcp://127.0.0.1:${RPC[$i]}" \
        --p2p.laddr "tcp://0.0.0.0:${P2P[$i]}" \
        --grpc.address "localhost:${GRPC[$i]}" \
        --api.enable --api.address "tcp://0.0.0.0:${API[$i]}" \
        $peers >"$BASE/n$i.log" 2>&1 & echo $! > "$BASE/n$i.pid" )
  done

  say "waiting for node0 to produce blocks"
  for _ in $(seq 60); do
    h=$(node0_height)
    [ "${h:-0}" -ge 3 ] && break
    sleep 1
  done

  say "funding + promoting node1, node2"
  alice=$("$EARTHD" keys show alice -a --keyring-backend test --home "$BASE/n0")
  for i in 1 2; do
    "$EARTHD" keys add "val$i" --keyring-backend test --home "$BASE/n$i" >/dev/null 2>&1
    addr=$("$EARTHD" keys show "val$i" -a --keyring-backend test --home "$BASE/n$i")
    tx "$EARTHD" tx bank send "$alice" "$addr" $(( ${STAKES[$i]} + 10000000 ))uerth \
      --from alice --keyring-backend test --home "$BASE/n0" \
      --node "tcp://127.0.0.1:${RPC[0]}" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --fees 5000uerth
    wait_blocks 2
  done

  for i in 1 2; do
    pubkey=$("$EARTHD" comet show-validator --home "$BASE/n$i")
    cat > "$BASE/n$i/val.json" <<EOF
{
  "pubkey": $pubkey,
  "amount": "${STAKES[$i]}uerth",
  "moniker": "node$i",
  "commission-rate": "0.10",
  "commission-max-rate": "0.20",
  "commission-max-change-rate": "0.01",
  "min-self-delegation": "1"
}
EOF
    tx "$EARTHD" tx staking create-validator "$BASE/n$i/val.json" \
      --from "val$i" --keyring-backend test --home "$BASE/n$i" \
      --node "tcp://127.0.0.1:${RPC[0]}" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --fees 5000uerth
    wait_blocks 2
  done

  say "up. RPC: ${RPC[*]}  logs: $BASE/n{0,1,2}.log"
}

down() {
  for i in 0 1 2; do
    [ -f "$BASE/n$i.pid" ] && kill "$(cat "$BASE/n$i.pid")" 2>/dev/null || true
  done
  pkill -f "earthd start --home $BASE" 2>/dev/null || true
  sleep 1
  rm -rf "$BASE"
  echo "==> down"
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 [up|down]" >&2; exit 2 ;;
esac
