# shellcheck shell=bash
#
# The human half of a rehearsal's governance vote, sourced by the
# rehearse-*.sh scripts.
#
# A proposal passes only with two thirds of the human votes cast (x/assembly),
# and one with no human vote never passes, so a rehearsal registers one human
# and casts its YES next to the validator's (tools/rehearsalhuman: a real
# passport proof, fee bundles and a membership proof, as a wallet makes them).
#
# Needs REPO, WORK and CHAIN_ID set, the mobile repo's circuits workspace
# (CIRCUITS, default ../earth-network-mobile/circuits next to this repo), and
# nargo and bb (v5.0.0).

CIRCUITS="${CIRCUITS:-$REPO/../earth-network-mobile/circuits}"

# The voting period a rehearsal genesis sets: room to prove and land the human
# vote (a membership proof and two action proofs) inside it.
RH_VOTING_PERIOD="90s"

# human_prepare GENESIS: builds the tool, proves the passport and patches
# GENESIS (its CSCA, two fee notes). Before the chain starts.
human_prepare() {
  [ -d "$CIRCUITS" ] || die "no circuits workspace at $CIRCUITS — set CIRCUITS to the mobile repo's circuits/"
  ( cd "$REPO" && go build -o "$WORK/rehearsalhuman" ./tools/rehearsalhuman ) \
    || die "building tools/rehearsalhuman failed"
  ( cd "$REPO" && "$WORK/rehearsalhuman" prepare -chain-id "$CHAIN_ID" -genesis "$1" \
      -circuits "$CIRCUITS" -work "$WORK/human" ) > "$WORK/human-prepare.log" 2>&1 \
    || die "preparing the rehearsal human failed — see $WORK/human-prepare.log"
  ok "passport proven for today; its CSCA and two fee notes are in genesis"
}

# human_register: registers the human. The chain must be producing blocks.
human_register() {
  "$WORK/rehearsalhuman" register -chain-id "$CHAIN_ID" -node tcp://127.0.0.1:26657 -work "$WORK/human" \
    > "$WORK/human-register.log" 2>&1 \
    || die "registering the rehearsal human failed — see $WORK/human-register.log"
  ok "one human registered ($(tail -1 "$WORK/human-register.log"))"
}

# human_vote PROPOSAL_ID: casts the human's YES on the proposal.
human_vote() {
  "$WORK/rehearsalhuman" vote -chain-id "$CHAIN_ID" -node tcp://127.0.0.1:26657 -work "$WORK/human" \
    -proposal "$1" > "$WORK/human-vote.log" 2>&1 \
    || die "the human vote failed — see $WORK/human-vote.log"
  ok "the human voted YES on proposal $1"
}

# wait_voting_end EARTHD QUERY_FLAGS PROPOSAL_ID: waits (up to 3 minutes) for
# the proposal to leave its voting period.
wait_voting_end() {
  local s=""
  for _ in $(seq 1 180); do
    s="$("$1" query gov proposal "$3" $2 -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["proposal"]["status"])' 2>/dev/null || true)"
    [ -n "$s" ] && [ "$s" != "PROPOSAL_STATUS_VOTING_PERIOD" ] && [ "$s" != "PROPOSAL_STATUS_DEPOSIT_PERIOD" ] && break
    sleep 1
  done
}

# current_height: the node's latest block height.
current_height() {
  curl -s --max-time 2 localhost:26657/status 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["sync_info"]["latest_block_height"])' 2>/dev/null
}
