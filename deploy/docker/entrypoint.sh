#!/usr/bin/env bash
#
# Starts an earth node against a genesis it verifies rather than one it invents.
#
# There are three paths and the difference between them is the difference
# between joining a network and creating one:
#
#   resume     $EARTH_HOME/config/genesis.json already exists. Start.
#   join       It does not. Copy the genesis baked into the image, check it
#              against the sha256 published with the release, start. No key is
#              created, no timestamp is rewritten, and every node that boots this
#              image computes the same genesis and the same app hash. This is
#              the default.
#   DEV_INIT=1 It does not, and you asked for a throwaway chain. Generate a
#              validator, stamp genesis_time to now, collect a gentx. Every node
#              doing this makes a DIFFERENT chain, which is exactly what a local
#              devnet wants and exactly what a network cannot tolerate.
#
# The join path is the whole point. The old behaviour was DEV_INIT with no way
# to turn it off: each node stamped its own genesis_time and minted its own
# validator, so two containers from the same image could never share a chain.
#
# State lives entirely under $EARTH_HOME, which is a mounted volume. The genesis
# check is what makes a redeploy resume the existing chain rather than silently
# starting a new one.
set -euo pipefail

EARTH_HOME="${EARTH_HOME:-/data}"
CHAIN_ID="${CHAIN_ID:-earth-1}"
MONIKER="${MONIKER:-earth-node}"
# The floor below which this node will not relay a transaction. Per-node config,
# not a chain parameter — there is no fee module, so the network's effective
# minimum is whatever most validators set, and changing it is a restart rather
# than a governance vote.
#
# 0uerth means free transactions, which means free spam. 0.005 is ~500uerth on a
# typical transaction — nothing to a user, especially since ads-for-gas sponsors
# them — while filling every block for a day would cost a spammer real ERTH.
MIN_GAS_PRICES="${MIN_GAS_PRICES:-0.005uerth}"

# Browser access, which the two RPC surfaces handle very differently.
#
# RPC_CORS_ORIGINS is a comma-separated allowlist for the CometBFT RPC (26657):
#
#   RPC_CORS_ORIGINS=https://app.erth.network,https://wallet.erth.network
#
# This is the one that matters for a CosmJS app. StargateClient talks to the RPC,
# not the LCD, and the RPC ships with cors_allowed_origins = [] — so a browser has
# never been able to reach it cross-origin, on any deployment of this chain. Use
# "*" only if you mean it.
RPC_CORS_ORIGINS="${RPC_CORS_ORIGINS:-}"
#
# API_UNSAFE_CORS opens the LCD (1317) to *any* origin. It is all-or-nothing
# because that is all the SDK offers — server/config has a bool and no allowlist —
# which is why it keeps the "unsafe" in its name. Prefer scoping the RPC above and
# pointing browsers there; reach for this only when something genuinely needs the
# REST surface from a page.
API_UNSAFE_CORS="${API_UNSAFE_CORS:-0}"

# Peering. The node already listens for p2p on 0.0.0.0:26656; these are what
# make it reachable and what give it somewhere to dial.
#
# EXTERNAL_ADDRESS is the address other nodes should use to reach this one,
# host:port. It matters more than it looks: without it CometBFT advertises the
# address it sees on itself, which inside a container is a private address, and
# it hands that to every peer through PEX. The node then appears in the network
# under an address nobody outside can dial. On Akash the provider maps 26656 to
# a port it chooses, so this has to be the provider's hostname and *that* port,
# which is only known once the lease is up.
EXTERNAL_ADDRESS="${EXTERNAL_ADDRESS:-}"
#
# SEEDS are crawlers that hand out peer addresses and then disconnect;
# PERSISTENT_PEERS are nodes to hold a connection to and redial. Both are
# comma-separated `nodeid@host:port`. A node with neither and no peer book has
# no way to find the network — the address book is the only other source, and on
# a fresh volume it is empty.
SEEDS="${SEEDS:-}"
PERSISTENT_PEERS="${PERSISTENT_PEERS:-}"

# Where the release genesis and its hash live in the image. Overridable only so
# the three paths below can be exercised without building a container — see
# deploy/docker/entrypoint_test.sh.
GENESIS_SRC="${GENESIS_SRC:-/etc/earth/genesis.json}"
GENESIS_SHA="${GENESIS_SHA:-${GENESIS_SRC}.sha256}"

# State-sync snapshots. This node produces them so that a NEW node can join by
# downloading state at a height instead of replaying every block from genesis.
#
# Worth more on this chain than on most. Replaying a block re-executes its
# transactions, and every passport registration verifies a zkSNARK
# (x/personhood, ultrahonk.Verify). A chain that mostly moves tokens replays
# quickly; this one re-runs a proof per registration, so sync cost grows with
# adoption rather than with time alone.
#
# The SDK default is 0 — off. A chain launched that way has no node offering
# snapshots, nobody can state-sync, and a snapshot cannot be produced for a
# height already passed. It is cheap now and unavailable later, which is the only
# reason it is on by default here.
#
# 1000 blocks is roughly 80 minutes at 5s. Keeping 5 gives a joining node a
# choice of recent heights without holding much disk.
SNAPSHOT_INTERVAL="${SNAPSHOT_INTERVAL:-1000}"
SNAPSHOT_KEEP_RECENT="${SNAPSHOT_KEEP_RECENT:-5}"

# Devnet-only. See the header: this makes a new chain, not a node on yours.
DEV_INIT="${DEV_INIT:-0}"
# What the devnet validator holds, and how much of it is bonded. Ignored unless
# DEV_INIT=1, because the join path creates no accounts.
VALIDATOR_COINS="${VALIDATOR_COINS:-1000000000000uerth}"
VALIDATOR_BONDED="${VALIDATOR_BONDED:-100000000uerth}"

say() { printf '[entrypoint] %s\n' "$*"; }
die() { printf '[entrypoint] FATAL: %s\n' "$*" >&2; exit 1; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# `sed -i` takes a mandatory suffix on BSD and a forbidden one on GNU, so the
# same invocation cannot work on both. The image is Linux, but this script is
# also run directly by its tests and by anyone debugging on a Mac.
# set_config writes `key = "value"` into a TOML file and checks that it landed.
#
# sed does nothing when its pattern misses, and says nothing about it. For the
# peering settings that silence is the worst outcome available: the node starts,
# logs that it is advertising an address, and is unreachable — which is the
# exact failure those settings exist to prevent. So the write is verified, and a
# miss stops the node instead of producing one that looks healthy from inside.
set_config() {
  local key="$1" value="$2" file="$3"
  sed_inplace "s|^$key = .*|$key = \"$value\"|" "$file"
  grep -q "^$key = \"$value\"$" "$file" || die "could not set $key in $file.
    There is no '$key = ' line to replace, which means this config was written
    by a version of CometBFT that spells it differently. Refusing to start
    rather than run with the setting silently ignored."
}

sed_inplace() {
  local expr="$1" file="$2"
  [ -f "$file" ] || die "$file is missing — the node home at $EARTH_HOME is not
    one earthd created. Point EARTH_HOME at a real node home, or delete it and
    let this script initialise one."
  sed "$expr" "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

if [ -f "$EARTH_HOME/config/genesis.json" ]; then
  say "existing genesis found — resuming chain at $EARTH_HOME"
  say "genesis sha256 $(sha256_of "$EARTH_HOME/config/genesis.json")"

elif [ "$DEV_INIT" = "1" ]; then
  # ── devnet ───────────────────────────────────────────────────────────────
  say "DEV_INIT=1 — creating a NEW throwaway chain (not joining one)"
  earthd init "$MONIKER" --chain-id "$CHAIN_ID" --home "$EARTH_HOME" >/dev/null 2>&1
  cp "$GENESIS_SRC" "$EARTH_HOME/config/genesis.json"

  # Stamp genesis_time to now. CometBFT gives block 1 exactly this time while
  # block 2 gets the wall clock, and the emission — plus the protocol-owned
  # liquidity retirement — is prorated against elapsed time, so a stale file
  # pays out the whole gap in a single block. Rewriting it is why this path
  # cannot be used for a real network: it changes the file, so the hash no
  # longer matches and no two nodes agree.
  NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  sed_inplace "s|\"genesis_time\":[[:space:]]*\"[^\"]*\"|\"genesis_time\": \"$NOW\"|" \
    "$EARTH_HOME/config/genesis.json"
  say "genesis_time stamped to $NOW"

  # --keyring-backend test writes the key unencrypted into the volume. That is
  # acceptable here and only here: this chain is disposable by construction. The
  # join path creates no keys at all, and a real validator's consensus key
  # belongs behind PRIV_VALIDATOR_LADDR.
  KEYRING="--keyring-backend test --home $EARTH_HOME"
  if [ -n "${VALIDATOR_MNEMONIC:-}" ]; then
    say "recovering the devnet validator key from VALIDATOR_MNEMONIC"
    printf '%s\n' "$VALIDATOR_MNEMONIC" | earthd keys add validator --recover $KEYRING >/dev/null
  else
    say "generating a devnet validator key — the mnemonic is printed once, below"
    earthd keys add validator $KEYRING --output json | tee /dev/stderr >/dev/null
    say "store that mnemonic: it is the only copy outside this volume"
  fi

  # add-genesis-account updates auth, bank balances and supply together, which
  # is why this is done with earthd rather than by editing the file.
  #
  # --append when the address is already funded, which it is whenever
  # VALIDATOR_MNEMONIC recovers the account the release genesis already carries.
  # Without this the command fails with "account already exists", the entrypoint
  # exits, and the container sits there running with nothing listening — the pod
  # reports available but never ready, and the tunnel serves 502 with no clue
  # why. That is a latent break: this path only runs on a fresh volume, so a
  # deployment that keeps resuming an existing chain never reaches it.
  VAL_ADDR="$(earthd keys show validator -a $KEYRING)"
  if grep -q "$VAL_ADDR" "$EARTH_HOME/config/genesis.json"; then
    say "validator $VAL_ADDR is already funded in the release genesis — appending"
    earthd genesis add-genesis-account validator "$VALIDATOR_COINS" --append $KEYRING
  else
    earthd genesis add-genesis-account validator "$VALIDATOR_COINS" $KEYRING
  fi
  earthd genesis gentx validator "$VALIDATOR_BONDED" --chain-id "$CHAIN_ID" $KEYRING >/dev/null
  earthd genesis collect-gentxs --home "$EARTH_HOME" >/dev/null 2>&1
  say "devnet genesis ready: validator bonded $VALIDATOR_BONDED"

else
  # ── join ─────────────────────────────────────────────────────────────────
  say "no genesis at $EARTH_HOME — installing the release genesis"
  [ -f "$GENESIS_SRC" ] || die "$GENESIS_SRC is missing from the image"
  [ -f "$GENESIS_SHA" ] || die "$GENESIS_SHA is missing from the image"

  WANT="$(awk '{print $1}' "$GENESIS_SHA")"
  GOT="$(sha256_of "$GENESIS_SRC")"
  if [ "$WANT" != "$GOT" ]; then
    die "genesis sha256 mismatch — refusing to start
      expected $WANT
      got      $GOT
    The genesis in this image is not the one it was built with. Do not work
    around this; get an image whose genesis matches the published hash."
  fi
  say "genesis sha256 $GOT — matches the release"

  earthd init "$MONIKER" --chain-id "$CHAIN_ID" --home "$EARTH_HOME" >/dev/null 2>&1
  cp "$GENESIS_SRC" "$EARTH_HOME/config/genesis.json"

  # Deliberately absent from this path: no genesis_time rewrite (it would change
  # the file the hash just vouched for), and no key generation (a validator joins
  # with MsgCreateValidator after height 1, or its gentx is already in the file).
  say "ready to join $CHAIN_ID — no keys created, genesis unmodified"
fi

# Remote signer, if one is configured.
#
# PRIV_VALIDATOR_LADDR turns the node into something that asks for signatures
# rather than something that can produce them: CometBFT listens on this address
# and an external signer (tmkms) dials in, signs, and returns just the
# signature. The consensus key then lives wherever the signer runs — which is
# the point, because this container runs on hardware someone else owns.
#
# Set in config.toml rather than passed as a flag: it is a config field, and
# writing it here means a restart cannot quietly fall back to the local key.
#
# The signer is the client, so it needs no inbound address of its own. That is
# what makes a home machine on a dynamic IP a viable place to keep the key.
#
# NOTE: setting this does not delete priv_validator_key.json. A real migration
# removes it from this host afterwards — leaving it behind means the key you
# just moved is still sitting on the machine you moved it off.
if [ -n "${PRIV_VALIDATOR_LADDR:-}" ]; then
  sed_inplace "s|^priv_validator_laddr = .*|priv_validator_laddr = \"$PRIV_VALIDATOR_LADDR\"|" \
    "$EARTH_HOME/config/config.toml"
  say "remote signer expected at $PRIV_VALIDATOR_LADDR"
fi

# Snapshots. Applied on every start for the same reason as the CORS settings:
# app.toml lives in the volume, so these can change with a restart.
#
# The default pruning profile keeps 362,880 recent states — far more than the
# snapshot interval — so a snapshot is never asked for a height that has already
# been pruned away. Change pruning and check that still holds.
sed_inplace "s|^snapshot-interval = .*|snapshot-interval = $SNAPSHOT_INTERVAL|" \
  "$EARTH_HOME/config/app.toml"
sed_inplace "s|^snapshot-keep-recent = .*|snapshot-keep-recent = $SNAPSHOT_KEEP_RECENT|" \
  "$EARTH_HOME/config/app.toml"
if [ "$SNAPSHOT_INTERVAL" = "0" ]; then
  say "snapshots OFF — no node can state-sync from this one"
else
  say "snapshots every $SNAPSHOT_INTERVAL blocks, keeping $SNAPSHOT_KEEP_RECENT"
fi

# RPC CORS. Applied on every start, not just first boot: config.toml lives in the
# volume, so an origin can be added or removed with a restart instead of a wipe.
if [ -n "$RPC_CORS_ORIGINS" ]; then
  # ["https://a", "https://b"] — a TOML array of quoted strings.
  RPC_CORS_TOML="$(printf '%s' "$RPC_CORS_ORIGINS" | awk -F, '{
    out = ""
    for (i = 1; i <= NF; i++) {
      gsub(/^[ \t]+|[ \t]+$/, "", $i)
      if ($i == "") continue
      out = out (out == "" ? "" : ", ") "\"" $i "\""
    }
    print "[" out "]"
  }')"
  sed_inplace "s|^cors_allowed_origins = .*|cors_allowed_origins = $RPC_CORS_TOML|" \
    "$EARTH_HOME/config/config.toml"
  say "rpc cors_allowed_origins = $RPC_CORS_TOML"
fi

# Peering, applied on every start like the settings above: config.toml is in the
# volume, so a seed can be added or an external address corrected with a restart
# rather than a wipe.
if [ -n "$EXTERNAL_ADDRESS" ]; then
  set_config external_address "$EXTERNAL_ADDRESS" "$EARTH_HOME/config/config.toml"
  say "advertising $EXTERNAL_ADDRESS to peers"
else
  say "EXTERNAL_ADDRESS unset — peers will be handed whatever address this node sees on itself, which in a container is usually unreachable"
fi
if [ -n "$SEEDS" ]; then
  set_config seeds "$SEEDS" "$EARTH_HOME/config/config.toml"
  say "seeds = $SEEDS"
fi
if [ -n "$PERSISTENT_PEERS" ]; then
  set_config persistent_peers "$PERSISTENT_PEERS" "$EARTH_HOME/config/config.toml"
  say "persistent_peers = $PERSISTENT_PEERS"
fi

# Bind to every interface. The defaults listen on loopback, which inside a
# container means nothing outside it can reach the node.
START_ARGS=(
  --home "$EARTH_HOME"
  --moniker "$MONIKER"
  --minimum-gas-prices "$MIN_GAS_PRICES"
  --rpc.laddr tcp://0.0.0.0:26657
  --p2p.laddr tcp://0.0.0.0:26656
  --api.enable
  --api.address tcp://0.0.0.0:1317
)
if [ "$API_UNSAFE_CORS" = "1" ]; then
  say "WARNING: API_UNSAFE_CORS=1 — any origin can read this LCD and broadcast through it"
  START_ARGS+=(--api.enabled-unsafe-cors)
fi

exec earthd start "${START_ARGS[@]}" "$@"
