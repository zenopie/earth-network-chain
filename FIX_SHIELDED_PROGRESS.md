# x/shielded audit fixes + Groundworks lazy weights (branch privacy/orchard)

Base: privacy/orchard bae86fa. Auditor PoCs: /private/tmp/claude-501/audit/shielded/.

## Status
- [x] 0 merge fix/staking (9e86f75) + fix/person (a7f69ab); genesis regenerated with make genesis; build/vet/test/genesis-check green
- [x] G Groundworks lazy position weights (per-validator weighted voter, no MaxPositions, min_position 1 ERTH)
- [x] M1 tx memo/timeout_height/gas_limit bound in every private sighash; timeout_timestamp refused; exact proof length; canonical bech32
- [x] M2 CheckTx verifies proofs sequentially, stops at first failure; cheap checks first; sentry rate-limit note
- [x] L1 max_private_actions_per_block doc; block gas bounds verification
- [x] L2 MintNote/PayFeeFromModule refuse fromModule == shielded
- [x] L3 shielded module account permissions
- [x] L4 ante events persist on failed txs (doc)
- [x] L5 release-map completeness (handlers declare released denoms)
- [x] Info: priority overflow, bb stderr log level, uppercase receiver
- [x] A one note-discovery rule (blind ciphertexts on every minted note; blind stake ciphertext)
- [x] B one fee rule (fee = uerth balance less what the msg moves; fee_from_output only for claims)
- [x] Re-record all fixtures; genesis; privacy-vks-check, genesis-check, go test ./...

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
- M1: orchard.TxFields{Memo, TimeoutHeight, GasLimit} bound after the
  digests (orchard.Sighash); types.WithTxFields/TxFieldsOf/SighashOf; the
  private ante records the fields; keeper, staking and personhood compute
  via SighashOf (fail closed without). timeout_timestamp refused. Gas limit
  bound rather than fixed: the real gas includes tx size and handler gas.
  ProofBytes = ultrahonk.ProofSize = 14,656 enforced everywhere. Canonical
  bech32: only MsgSend.receiver, personhood affiliate/referrer, staking
  valoper carry addresses; all canonical (fix/staking F0 + this).
- M2: orchard.VerifyProofsSequential in CheckTx; keeper.proofVerifier seam
  (export_test WithProofVerifier). Tests: x/shielded/keeper
  TestCheckPrivateMsgAuditOneJunkProof (1 vs 16 verifications),
  app TestShieldedAuditCheckTxForgedBundle, zk/ultrahonk
  TestAuditSequentialVerificationStopsAtFirstFailure.
- L1: docs (ante, action.go, params.proto); TestShieldedAuditBlockGasBounds
  FailedVerification (6 forged 16-action txs: 2 verified, 4 refused).
- L2: notThePool in MintNote/ReleaseToModule/PayFeeFromModule
  (TestAuditPoolCannotPayItself). L3: no perms on the pool account
  (TestShieldedAuditPoolAccountHasNoMintBurn; auditFundPool shields now).
- L4: ante/ORCHARD_DESIGN docs. L5: ReleasedDenoms on every handler,
  checkReleaseMap (TestDexAuditReleaseMapMatchesHandler).
- Info: priority cap; bb log level (third_party barretenberg-go
  SetLogLevel; TestBarretenbergLogLevelLowered); proof length tests.
- A: privacy.EncryptBlindStakeNote/DecryptBlindStakeNote (golden vector,
  Python cross-checked); shieldedtypes.CheckBlindCiphertext in noteFor
  (Shield, MintNote, CheckMint), mintStake, every msg's ValidateBasic,
  dex genesis; ciphertext on mint/shield/stake events;
  StakeProof.spc_ciphertext (field 8). 
- B: shieldedtypes.UerthBalance/FeeAfter; proto fields removed/added per
  ORCHARD_DESIGN §14; dex executeNoteSwap no longer pays from output.
- Fixtures re-recorded: shielded (43), personhood passports + app, staking
  (111), dex (19), orchard bundles 1/2/3/10, membership parity.
