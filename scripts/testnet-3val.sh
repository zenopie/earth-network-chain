#!/usr/bin/env bash
#
# Brings up a local 3-validator network and leaves it running.
#
#   ./scripts/testnet-3val.sh up      # build genesis, start 3 nodes, promote 2
#   ./scripts/testnet-3val.sh down    # stop everything and clean up
#
# Node 0 is the genesis validator (from config.yml via `ignite chain init`, so it
# carries the seeded CSCAs, verifying keys and the ANML/ERTH pool). Nodes 1 and 2
# sync, then join the validator set with MsgCreateValidator — the same path a
# real operator takes, which also exercises validator-set changes at runtime
# rather than only at genesis.
#
# Voting power is deliberately uneven (see STAKES): it makes the >2/3 liveness
# threshold observable — losing the largest validator halts the chain, losing a
# smaller one does not.
set -euo pipefail

CHAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EARTHD="${EARTHD:-$HOME/go/bin/earthd}"
CHAIN_ID=earth-1
BASE=/tmp/earth-3val

# node index -> RPC / P2P / API / gRPC ports
RPC=(26657 26667 26677)
P2P=(26656 26666 26676)
API=(1317 1327 1337)
GRPC=(9090 9190 9290)
# uerth each joining validator self-delegates; node 0's stake comes from genesis.
STAKES=(0 300000000 50000000)

say() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

up() {
  command -v ignite >/dev/null || { echo "error: ignite not on PATH" >&2; exit 1; }
  [ -x "$EARTHD" ] || { echo "error: no earthd at $EARTHD" >&2; exit 1; }

  rm -rf "$BASE"; mkdir -p "$BASE"

  say "node0: building genesis via ignite chain init (~1 min, silent)"
  ( cd "$CHAIN_DIR" && ignite chain init --home "$BASE/n0" >"$BASE/init.log" 2>&1 ) \
    || { echo "ignite chain init failed; see $BASE/init.log" >&2; exit 1; }
  say "node0: genesis ready"

  node0_id=$("$EARTHD" tendermint show-node-id --home "$BASE/n0")

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
    h=$(curl -s "http://127.0.0.1:${RPC[0]}/status" 2>/dev/null \
        | python3 -c "import json,sys;print(json.load(sys.stdin)['result']['sync_info']['latest_block_height'])" 2>/dev/null || echo 0)
    [ "${h:-0}" -ge 3 ] && break
    sleep 1
  done

  say "funding + promoting node1, node2"
  alice=$("$EARTHD" keys show alice -a --keyring-backend test --home "$BASE/n0")
  for i in 1 2; do
    "$EARTHD" keys add "val$i" --keyring-backend test --home "$BASE/n$i" >/dev/null 2>&1
    addr=$("$EARTHD" keys show "val$i" -a --keyring-backend test --home "$BASE/n$i")
    "$EARTHD" tx bank send "$alice" "$addr" $(( ${STAKES[$i]} + 10000000 ))uerth \
      --from alice --keyring-backend test --home "$BASE/n0" \
      --node "tcp://127.0.0.1:${RPC[0]}" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --fees 5000uerth -y >/dev/null
    sleep 4
  done

  for i in 1 2; do
    pubkey=$("$EARTHD" tendermint show-validator --home "$BASE/n$i")
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
    "$EARTHD" tx staking create-validator "$BASE/n$i/val.json" \
      --from "val$i" --keyring-backend test --home "$BASE/n$i" \
      --node "tcp://127.0.0.1:${RPC[0]}" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --fees 5000uerth -y >/dev/null
    sleep 4
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
