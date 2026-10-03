# x/shielded audit fixes + Groundworks lazy weights (branch privacy/orchard)

Base: privacy/orchard bae86fa. Auditor PoCs: /private/tmp/claude-501/audit/shielded/.

## Status
- [x] 0 merge fix/staking (9e86f75) + fix/person (a7f69ab); genesis regenerated with make genesis; build/vet/test/genesis-check green
- [x] G Groundworks lazy position weights (per-validator weighted voter, no MaxPositions, min_position 1 ERTH)
- [ ] M1 tx memo/timeout_height/gas_limit bound in every private sighash; timeout_timestamp refused; exact proof length; canonical bech32
- [ ] M2 CheckTx verifies proofs sequentially, stops at first failure; cheap checks first; sentry rate-limit note
- [ ] L1 max_private_actions_per_block doc; block gas bounds verification
- [ ] L2 MintNote/PayFeeFromModule refuse fromModule == shielded
- [ ] L3 shielded module account permissions
- [ ] L4 ante events persist on failed txs (doc)
- [ ] L5 release-map completeness (handlers declare released denoms)
- [ ] Info: priority overflow, bb stderr log level, uppercase receiver
- [ ] Re-record all fixtures; genesis; privacy-vks-check, genesis-check, go test ./...

## Decisions
- M1: bind gas_limit (not "gas must equal X"): a private tx's real gas includes
  tx-size gas and the handler's, so no fixed formula exists; a relayer that
  lowers the limit could make the handler run out of gas after the ante wrote.
  timeout_timestamp is refused outright for private txs (unordered is already
  refused) instead of bound.
- Proof length: every bb v5.0.0 UltraHonk (ZK flavor) proof is 14656 bytes
  whatever the circuit (constant proof size); bb ignored trailing bytes
  (len 14657 verified). Exact length enforced in zk/ultrahonk.Verify and in
  every stateless check.
- bb log spam: bb_log_level (int, default 4 = info; BB_VERBOSE=1 -> 5) set to
  3 at init so failed verifications stop printing "UltraVerifier:
  verification failed" to stderr.

## Log
- G (user-approved addition): x/allocation Voter.option_weights (field 4) +
  OptionWeight; writeVoter generalizes resyncVoter (old/new contributions
  from a split or absolute weights); SetWeightedVoter, StreamEpoch;
  ResyncVoter leaves weighted voters alone; genesis validates them.
  x/shieldedstaking: GwTotals (prefix 29) per (valoper, option) =
  sum derth x pct (no /100: exact); GwEpoch (30) per validator; one voter
  per validator, key "gwpos/"||val bytes, weights trunc(rate x T / 100);
  applyPositionSplit for Lock/Update/Unlock; ReweighGroundworks at epoch
  end, per processed book in the sweep, and on slash; reset honoured
  lazily via Position.split_epoch (field 10) + GwEpoch. Removed
  PositionVoterKey, reweighPosition(s), PositionCount (prefix 26 retired),
  Params.max_positions (field 3 reserved); min_position 1 ERTH. Position
  weight filled in by queries/events. Invariant 6 (totals == positions);
  reportInvariants counts positions against InvariantBookLimit.
  Tests: app/groundworks_lazy_test.go (equivalence, exactness, 300
  positions no cap, epoch gas 85,025 vs 85,856 for 1 vs 200 positions,
  slash, lazy reset, genesis round trip; handlers driven with the ante
  faked), x/allocation/keeper/weighted_voter_test.go; existing Groundworks
  app tests moved to the validator voter.
