#!/usr/bin/env bash
#
# Exercises entrypoint.sh's three paths without building a container.
#
# This script decides whether a node joins your network or forks off its own, and
# the failure is silent: a node that invents its own genesis starts, produces
# blocks, and looks healthy right up until you notice it shares a chain with
# nobody. That is worth a test even though it is shell.
#
# earthd is stubbed rather than real. What is under test is the branching, the
# hash check and the flags — not whether the SDK can init a node, which it can.
#
#   docker/entrypoint_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
ENTRYPOINT="$HERE/entrypoint.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok()  { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n     %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# ── a stub earthd that records what it was asked to do ─────────────────────
mkdir -p "$WORK/bin"
cat > "$WORK/bin/earthd" <<'STUB'
#!/usr/bin/env bash
echo "earthd $*" >> "$EARTHD_LOG"
case "$1" in
  init)
    home=""; for ((i=1;i<=$#;i++)); do [ "${!i}" = "--home" ] && j=$((i+1)) && home="${!j}"; done
    mkdir -p "$home/config"
    # Enough of a real config.toml for the settings the entrypoint writes,
    # persistent_peers_max_dial_period included: it shares a prefix with
    # persistent_peers, and a pattern loose enough to hit both would leave
    # CometBFT unable to parse its own config.
    printf 'priv_validator_laddr = ""\ncors_allowed_origins = []\nexternal_address = ""\nseeds = ""\npersistent_peers = ""\npersistent_peers_max_dial_period = "0s"\n' > "$home/config/config.toml"
    printf 'snapshot-interval = 0\nsnapshot-keep-recent = 2\n' > "$home/config/app.toml"
    printf '{"stock":true}\n' > "$home/config/genesis.json"
    ;;
  start) echo "STARTED $*" >> "$EARTHD_LOG" ;;
esac
exit 0
STUB
chmod +x "$WORK/bin/earthd"
export PATH="$WORK/bin:$PATH"

# ── a genesis and a matching hash, standing in for the image's ─────────────
GEN="$WORK/genesis.json"
cp "$REPO/networks/genesis.json" "$GEN"
if command -v sha256sum >/dev/null 2>&1; then sha256sum "$GEN" | awk '{print $1}' > "$GEN.sha256"
else shasum -a 256 "$GEN" | awk '{print $1}' > "$GEN.sha256"; fi

run() { # run <home> [env...] -> writes $LOG, returns entrypoint's exit code
  local home="$1"; shift
  export EARTHD_LOG="$WORK/earthd.log"; : > "$EARTHD_LOG"
  LOG="$WORK/out.log"
  env EARTH_HOME="$home" GENESIS_SRC="$GEN" "$@" bash "$ENTRYPOINT" >"$LOG" 2>&1
}

# ── 1. join: the default, and the whole point ──────────────────────────────
H="$WORK/join"; mkdir -p "$H"
if run "$H" ; then
  grep -q "matches the release" "$LOG" \
    && ok "join: verifies the genesis hash" \
    || bad "join: did not report a hash match" "$(tail -2 "$LOG")"

  if cmp -s "$GEN" "$H/config/genesis.json"; then
    ok "join: genesis is installed byte-identical"
  else
    bad "join: genesis was modified" "a rewritten file no longer matches its published hash"
  fi

  grep -q "keys add" "$EARTHD_LOG" \
    && bad "join: created a key" "the join path must not mint a validator" \
    || ok "join: creates no keys"

  grep -q "genesis gentx\|collect-gentxs" "$EARTHD_LOG" \
    && bad "join: made its own gentx" "every node would compute a different genesis" \
    || ok "join: makes no gentx"

  grep -q "api.enabled-unsafe-cors" "$EARTHD_LOG" \
    && bad "join: CORS left open" "any origin could read and broadcast" \
    || ok "join: unsafe CORS is off by default"

  grep -q "p2p.laddr tcp://0.0.0.0:26656" "$EARTHD_LOG" \
    && ok "join: p2p binds on all interfaces" \
    || bad "join: p2p not bound" "$(grep STARTED "$EARTHD_LOG" | head -1)"
else
  bad "join: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 2. join with a tampered genesis: must refuse ───────────────────────────
H="$WORK/tampered"; mkdir -p "$H"
BAD_GEN="$WORK/bad.json"; cp "$GEN" "$BAD_GEN"; printf '\n' >> "$BAD_GEN"
cp "$GEN.sha256" "$BAD_GEN.sha256"     # hash of the ORIGINAL, deliberately
export EARTHD_LOG="$WORK/earthd.log"; : > "$EARTHD_LOG"
if env EARTH_HOME="$H" GENESIS_SRC="$BAD_GEN" bash "$ENTRYPOINT" >"$WORK/out.log" 2>&1; then
  bad "tampered: started anyway" "a swapped genesis must not boot"
else
  grep -q "sha256 mismatch" "$WORK/out.log" \
    && ok "tampered: refuses to start, and says why" \
    || bad "tampered: failed for the wrong reason" "$(tail -3 "$WORK/out.log")"
  grep -q "STARTED" "$EARTHD_LOG" \
    && bad "tampered: reached earthd start" "it must fail before starting" \
    || ok "tampered: never reaches start"
fi

# ── 3. DEV_INIT=1: the old behaviour, now opt-in ───────────────────────────
H="$WORK/dev"; mkdir -p "$H"
if run "$H" DEV_INIT=1; then
  grep -q "keys add validator" "$EARTHD_LOG" \
    && ok "dev: creates a validator key" \
    || bad "dev: no key created" "$(tail -3 "$LOG")"
  grep -q "collect-gentxs" "$EARTHD_LOG" \
    && ok "dev: collects a gentx" \
    || bad "dev: no gentx" "$(tail -3 "$LOG")"
  grep -q '"genesis_time": "' "$H/config/genesis.json" \
    && ok "dev: stamps genesis_time" \
    || bad "dev: genesis_time not stamped" "emission would pay out the whole gap"
  cmp -s "$GEN" "$H/config/genesis.json" \
    && bad "dev: genesis unchanged" "the timestamp should have been rewritten" \
    || ok "dev: genesis is modified, so it cannot be mistaken for the release"
else
  bad "dev: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 4. resume: an existing genesis is never replaced ───────────────────────
H="$WORK/resume"; mkdir -p "$H/config"
printf '{"mine":true}\n' > "$H/config/genesis.json"
printf 'priv_validator_laddr = ""\ncors_allowed_origins = []\n' > "$H/config/config.toml"
printf 'snapshot-interval = 1000\nsnapshot-keep-recent = 5\n' > "$H/config/app.toml"
if run "$H"; then
  grep -q "resuming chain" "$LOG" && ok "resume: detects existing state" \
    || bad "resume: did not resume" "$(tail -2 "$LOG")"
  grep -q '"mine":true' "$H/config/genesis.json" \
    && ok "resume: leaves the existing genesis alone" \
    || bad "resume: overwrote the chain's genesis" "this would fork an existing node"
else
  bad "resume: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 5. CORS is reachable when actually asked for ───────────────────────────
H="$WORK/cors"; mkdir -p "$H"
if run "$H" API_UNSAFE_CORS=1; then
  grep -q "api.enabled-unsafe-cors" "$EARTHD_LOG" \
    && ok "cors: opt-in works" || bad "cors: opt-in ignored" "$(grep STARTED "$EARTHD_LOG")"
  grep -q "WARNING" "$LOG" \
    && ok "cors: warns when enabled" || bad "cors: enabled silently" ""
else
  bad "cors: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 6. RPC CORS allowlist ──────────────────────────────────────────────────
#
# The gap this closes: CosmJS talks to the RPC, not the LCD, and the RPC ships
# with cors_allowed_origins = [] — so a browser has never been able to reach it.
# Confirmed against the live devnet: rpc.erth.network returns no CORS headers at
# all, from the node or from Cloudflare.
H="$WORK/rpccors"; mkdir -p "$H"
if run "$H" RPC_CORS_ORIGINS="https://app.erth.network, https://wallet.erth.network"; then
  want='cors_allowed_origins = ["https://app.erth.network", "https://wallet.erth.network"]'
  got="$(grep '^cors_allowed_origins' "$H/config/config.toml" || true)"
  [ "$got" = "$want" ] \
    && ok "rpc cors: writes a scoped allowlist, trimming whitespace" \
    || bad "rpc cors: wrong TOML" "want: $want
     got:  $got"
else
  bad "rpc cors: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# Unset must leave the default alone — an accidental "*" here is the whole
# problem this is meant to avoid.
H="$WORK/rpccors-off"; mkdir -p "$H"
if run "$H"; then
  grep -q '^cors_allowed_origins = \[\]$' "$H/config/config.toml" \
    && ok "rpc cors: closed unless asked for" \
    || bad "rpc cors: default changed" "$(grep '^cors_allowed_origins' "$H/config/config.toml")"
else
  bad "rpc cors off: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# It is applied on restart too, so an origin can be changed without a wipe.
H="$WORK/rpccors-resume"; mkdir -p "$H/config"
printf '{"mine":true}\n' > "$H/config/genesis.json"
printf 'priv_validator_laddr = ""\ncors_allowed_origins = ["https://old.example"]\n' > "$H/config/config.toml"
printf 'snapshot-interval = 1000\nsnapshot-keep-recent = 5\n' > "$H/config/app.toml"
if run "$H" RPC_CORS_ORIGINS="https://new.example"; then
  grep -q 'cors_allowed_origins = \["https://new.example"\]' "$H/config/config.toml" \
    && ok "rpc cors: re-applied on an existing volume" \
    || bad "rpc cors: stale on restart" "$(grep '^cors_allowed_origins' "$H/config/config.toml")"
else
  bad "rpc cors resume: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 7. state-sync snapshots ────────────────────────────────────────────────
#
# The SDK default is 0, meaning this node offers no snapshots and nobody can
# state-sync from it. A snapshot cannot be produced for a height already passed,
# so launching with it off is a decision that cannot be revisited later.
H="$WORK/snap"; mkdir -p "$H"
if run "$H"; then
  grep -q '^snapshot-interval = 1000$' "$H/config/app.toml" \
    && ok "snapshots: on by default" \
    || bad "snapshots: not enabled" "$(grep '^snapshot-' "$H/config/app.toml")"
  grep -q '^snapshot-keep-recent = 5$' "$H/config/app.toml" \
    && ok "snapshots: keeps 5" \
    || bad "snapshots: wrong keep-recent" "$(grep '^snapshot-keep' "$H/config/app.toml")"
else
  bad "snapshots: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# Overridable, including off — and turning them off should be loud, because a
# node with snapshots off looks identical to one with them on until someone
# tries to sync from it.
H="$WORK/snap-off"; mkdir -p "$H"
if run "$H" SNAPSHOT_INTERVAL=0; then
  grep -q '^snapshot-interval = 0$' "$H/config/app.toml" \
    && ok "snapshots: can be turned off" \
    || bad "snapshots: override ignored" ""
  grep -q "snapshots OFF" "$LOG" \
    && ok "snapshots: says so when off" || bad "snapshots: turned off silently" ""
else
  bad "snapshots off: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# Re-applied on an existing volume, so the cadence can change with a restart.
H="$WORK/snap-resume"; mkdir -p "$H/config"
printf '{"mine":true}\n' > "$H/config/genesis.json"
printf 'priv_validator_laddr = ""\ncors_allowed_origins = []\n' > "$H/config/config.toml"
printf 'snapshot-interval = 1000\nsnapshot-keep-recent = 5\n' > "$H/config/app.toml"
if run "$H" SNAPSHOT_INTERVAL=500; then
  grep -q '^snapshot-interval = 500$' "$H/config/app.toml" \
    && ok "snapshots: re-applied on an existing volume" \
    || bad "snapshots: stale on restart" "$(grep '^snapshot-interval' "$H/config/app.toml")"
else
  bad "snapshots resume: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# ── 8. peering ─────────────────────────────────────────────────────────────
#
# The failure this covers is invisible from inside the container: without
# external_address CometBFT advertises the address it sees on itself, which here
# is a private one, and hands it to every peer through PEX. The node dials out,
# looks healthy, and can never be dialled back.
H="$WORK/peers"; mkdir -p "$H"
if run "$H" EXTERNAL_ADDRESS="203.0.113.7:26656" \
       SEEDS="aaaa@seed.erth.network:26656" \
       PERSISTENT_PEERS="bbbb@peer.one:26656,cccc@peer.two:26656"; then
  grep -q '^external_address = "203.0.113.7:26656"$' "$H/config/config.toml" \
    && ok "peers: advertises the address it was given" \
    || bad "peers: external_address not written" "$(grep '^external_address' "$H/config/config.toml")"
  grep -q '^seeds = "aaaa@seed.erth.network:26656"$' "$H/config/config.toml" \
    && ok "peers: seeds written" || bad "peers: seeds not written" "$(grep '^seeds' "$H/config/config.toml")"
  grep -q '^persistent_peers = "bbbb@peer.one:26656,cccc@peer.two:26656"$' "$H/config/config.toml" \
    && ok "peers: persistent_peers written" \
    || bad "peers: persistent_peers not written" "$(grep '^persistent_peers = ' "$H/config/config.toml")"
  # The name is a prefix of persistent_peers_max_dial_period, which a looser
  # pattern would overwrite with a peer list and leave CometBFT refusing to load.
  grep -q '^persistent_peers_max_dial_period = "0s"$' "$H/config/config.toml" \
    && ok "peers: leaves persistent_peers_max_dial_period alone" \
    || bad "peers: clobbered a neighbouring key" "$(grep '^persistent_peers_max' "$H/config/config.toml")"
  grep -q "advertising 203.0.113.7:26656" "$LOG" \
    && ok "peers: says what it advertises" || bad "peers: silent about its address" ""
else
  bad "peers: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# Unset is the dangerous default, so it has to be loud rather than absent.
H="$WORK/peers-off"; mkdir -p "$H"
if run "$H"; then
  grep -q '^external_address = ""$' "$H/config/config.toml" \
    && ok "peers: no address invented when none is given" \
    || bad "peers: external_address changed unasked" "$(grep '^external_address' "$H/config/config.toml")"
  grep -q "EXTERNAL_ADDRESS unset" "$LOG" \
    && ok "peers: warns that it will be unreachable" \
    || bad "peers: unreachable silently" "the whole failure is that it looks fine"
else
  bad "peers off: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

# Applied on restart, so a seed can be added or a wrong address corrected
# without destroying the volume — which on this deployment is the chain.
H="$WORK/peers-resume"; mkdir -p "$H/config"
printf '{"mine":true}\n' > "$H/config/genesis.json"
printf 'priv_validator_laddr = ""\ncors_allowed_origins = []\nexternal_address = "wrong:1"\nseeds = ""\npersistent_peers = ""\n' > "$H/config/config.toml"
printf 'snapshot-interval = 1000\nsnapshot-keep-recent = 5\n' > "$H/config/app.toml"
if run "$H" EXTERNAL_ADDRESS="198.51.100.4:26656" SEEDS="dddd@seed.two:26656"; then
  grep -q '^external_address = "198.51.100.4:26656"$' "$H/config/config.toml" \
    && ok "peers: re-applied on an existing volume" \
    || bad "peers: stale on restart" "$(grep '^external_address' "$H/config/config.toml")"
else
  bad "peers resume: entrypoint exited non-zero" "$(tail -3 "$LOG")"
fi

printf '\n  %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
