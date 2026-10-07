#!/usr/bin/env bash
#
# Exercises docker/entrypoint.sh without building a container. earthd is
# stubbed: what is under test is the first-start/resume branching, the
# genesis hash check and the arguments `earthd start` gets.
#
#   docker/entrypoint_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
ENTRYPOINT="$HERE/entrypoint.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok()  { printf '  ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  FAIL %s\n       %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

mkdir -p "$WORK/bin"
cat > "$WORK/bin/earthd" <<'STUB'
#!/usr/bin/env bash
echo "earthd $*" >> "$EARTHD_LOG"
case "$1" in
  init)
    home=""; for ((i=1;i<=$#;i++)); do [ "${!i}" = "--home" ] && j=$((i+1)) && home="${!j}"; done
    mkdir -p "$home/config"
    printf '{"stock":true}\n' > "$home/config/genesis.json"
    printf '{"priv_key":"random"}\n' > "$home/config/priv_validator_key.json"
    ;;
esac
exit 0
STUB
chmod +x "$WORK/bin/earthd"
export PATH="$WORK/bin:$PATH"

GEN="$WORK/genesis.json"
cp "$REPO/networks/genesis.json" "$GEN"
cp "$REPO/networks/genesis.json.sha256" "$GEN.sha256"

run() { # run <home> [args...]: entrypoint output in $LOG, earthd calls in $EARTHD_LOG
  local home="$1"; shift
  export EARTHD_LOG="$WORK/earthd.log"; : > "$EARTHD_LOG"
  LOG="$WORK/out.log"
  EARTH_HOME="$home" GENESIS_SRC="$GEN" bash "$ENTRYPOINT" "$@" >"$LOG" 2>&1
}

# ── first start: the release genesis, verified, installed byte for byte ────
H="$WORK/fresh"
if run "$H"; then
  cmp -s "$GEN" "$H/config/genesis.json" \
    && ok "first start: installs the release genesis unmodified" \
    || bad "first start: genesis differs from the release"
  grep -q "^earthd init earth-node --chain-id earth-1 --home $H$" "$EARTHD_LOG" \
    && ok "first start: inits with the genesis's chain id" \
    || bad "first start: wrong init" "$(grep '^earthd init' "$EARTHD_LOG")"
  grep -q "keys add\|gentx\|collect-gentxs" "$EARTHD_LOG" \
    && bad "first start: made a key or a gentx" "" \
    || ok "first start: no keys, no gentx"
  grep -q "^earthd start --home $H --rpc.laddr tcp://0.0.0.0:26657 --api.enable --api.address tcp://0.0.0.0:1317$" "$EARTHD_LOG" \
    && ok "first start: starts with RPC and LCD reachable" \
    || bad "first start: wrong start" "$(grep '^earthd start' "$EARTHD_LOG")"
else
  bad "first start: exited non-zero" "$(tail -3 "$LOG")"
fi

# ── a genesis that does not match its published hash: refused ──────────────
H="$WORK/tampered"
printf '\n' >> "$GEN"
if run "$H"; then
  bad "tampered: started anyway"
else
  grep -q "sha256 mismatch" "$LOG" && ok "tampered: refuses, and says why" \
    || bad "tampered: failed for another reason" "$(tail -3 "$LOG")"
  grep -q "^earthd start" "$EARTHD_LOG" && bad "tampered: reached earthd start" "" \
    || ok "tampered: never starts"
fi
cp "$REPO/networks/genesis.json" "$GEN"

# ── resume: the image's own chain; flags pass through ──────────────────────
H="$WORK/resume"; mkdir -p "$H/config"
cp "$GEN" "$H/config/genesis.json"
if run "$H" --minimum-gas-prices 0.01uerth --log_level error; then
  grep -q "^earthd init" "$EARTHD_LOG" && bad "resume: re-initialised" "" || ok "resume: no init"
  grep -q -- "--api.address tcp://0.0.0.0:1317 --minimum-gas-prices 0.01uerth --log_level error$" "$EARTHD_LOG" \
    && ok "resume: container arguments reach earthd start, last" \
    || bad "resume: arguments lost" "$(grep '^earthd start' "$EARTHD_LOG")"
else
  bad "resume: exited non-zero" "$(tail -3 "$LOG")"
fi

# ── resume on another chain's volume: refused unless overridden ────────────
H="$WORK/foreign"; mkdir -p "$H/config"
printf '{"another":"chain"}\n' > "$H/config/genesis.json"
if run "$H"; then
  bad "foreign: started on another chain's volume"
else
  grep -q "holds another chain's genesis: refusing to start" "$LOG" \
    && ok "foreign: refuses, and says why" \
    || bad "foreign: failed for another reason" "$(tail -5 "$LOG")"
  grep -q "^earthd" "$EARTHD_LOG" && bad "foreign: ran earthd" "" || ok "foreign: never starts"
  grep -q '"another":"chain"' "$H/config/genesis.json" \
    && ok "foreign: leaves the volume's data alone" \
    || bad "foreign: touched the volume's genesis"
fi
export EARTH_ALLOW_FOREIGN_GENESIS=1
if run "$H"; then
  grep -q "EARTH_ALLOW_FOREIGN_GENESIS=1, starting anyway" "$LOG" \
    && ok "foreign + override: starts, loudly" \
    || bad "foreign + override: silent" "$(cat "$LOG")"
  grep -q '"another":"chain"' "$H/config/genesis.json" \
    && ok "foreign + override: the volume's genesis is never replaced" \
    || bad "foreign + override: replaced the volume's genesis"
else
  bad "foreign + override: exited non-zero" "$(tail -3 "$LOG")"
fi
unset EARTH_ALLOW_FOREIGN_GENESIS

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
