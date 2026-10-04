# Audit round 6: chain fixes

Branch privacy/orchard from c0ad1dd. Reports: audit6-chain-{A,B,C,D}.md
(scratchpad). Fresh-genesis relaunch: no migrations. Feature freeze: fixes
only. Principle: people are private; power and public money are public.
Wallet-facing rules: ORCHARD_DESIGN.md section 17. No circuit changed: VKs
and genesis.json unchanged (sha256 77af758697b293eb95d1f9f08a8f49b35bef648fd931ae1448cc3d0f2ddd652d).

## Must fix
- [x] D6-1 (High) allocation: resyncFromBonded computes the weight as the
  SDK's GetDelegatorBonded sum with the removed delegation left out, whatever
  the validator's status (bondedWeight). Status changes move no tokens or
  shares, so need no resync; slashes resync at EndBlock; a redelegation is
  refused by the sole-delegator guard. Tests:
  app/audit6_groundworks_weight_test.go (same-block create/vote/undelegate,
  jailed -> unbonding -> partial -> unbonded -> full, bonded, redelegation).
- [x] X-1 (Medium): ACCEPTED by the user (they were the only user on the old
  chains); the account-number offset was written and reverted, nothing
  committed.
- [x] B6-1 (Medium): a switch whose proof's DSC differs from the live
  registration's is refused (ErrSwitchSignerMismatch, 1127). Chosen over
  rate-capping it: a genuine re-proof is always signed by the same DSC, and
  refusing also closes purge evasion. A switch counts against its signer's
  daily cap (the DscRate counter, shared with registrations); the network and
  country counters do not move. Passport fixture A2 now re-proves A1's
  passport under A1's DSC (tools/poafixtures signer=). Tests: gascheck_test.
- [x] Handle owner (wallet dependency): HandleEntry.owner (hex, field 6) in
  Query/Handle and Query/Handles; handle_bound/handle_moved/handle_released
  carry owner, handle_moved previous_owner. Already public (bind membership
  nullifier, move new_owner/membership nullifier). Test: TestPrivatePersonhood.

## Lows
- [x] A-L1: gas_limit <= 5 x gas used (PrivateGasCeilingFactor), checked in
  the private ante before the proofs. A tighter used+margin bound needs every
  test harness re-priced per tx and every fixture set reproven; 5x covers
  the harnesses' fixed limits (max seen 4.54x) and wallets (+10%).
- [x] A-L2: SendEnabled in ReleaseToModule, MintNote (so MintNoteSplit),
  MintOpenNote, checkReleaseMap.
- [x] A-L3: one anchor per bundle (Bundle.ValidateBasic).
- [x] A-I1/A-I2: soundness comment (63-bit), Action.anchor proto comment.
- [x] A-I5: max_private_actions_per_block <= 256.
- [x] A-I3: bb bindings lock the OS thread around the C call and its error read.
- [x] B6-2: putIdentityRoot removes a recurring root's stale by-time entry.
- [x] B6-3: InitGenesis refuses registered_at/activated_at/predecessor_at
  after genesis and handle expires_at past genesis + handle_lease_max.
- [x] B6-4: RegistrationBinding binds Bytes(chain_id) (no circuit change;
  passport and personhood app fixtures regenerated).
- [x] B6-5: SweepRetry: a registration the expiry/purge sweep fails to retire
  is passed over for a day (scan bounded at 8 x budget), then retried. The
  expired-but-unswept proving lag is documented (section 17).
- [x] B6-7: genesis registration nullifier 1..32 bytes (as passports_seen).
- [x] C-L1: reweighSlashed skips a validator with no book.
- [x] C-L2: checkLock fitsNote.
- [x] C-L3: MsgStakeVote weight <= 3 significant digits (RoundVoteWeight);
  staking fixtures regenerated.
- [x] C-L4: RootsStale set on a failed root recording; a snapshot taken
  while set takes no roots (no note votes on that proposal). Done without a
  proto field: the auditor's NfRootHeight rule needs the recording height
  in the snapshot and in genesis export/zero-height shifts.
- [x] C-I2: genesis requires a book for every module delegation.
- [x] D-L-A1: voter splits drop pruned options at resync and in the export.
- [x] D-L-D1: a payout that would pass the note budget waits (counted before
  minting; the sweep's first payout always runs).
- [x] D-L-D2: volume_depth_cap_per_day <= 1,000.
- [x] D-L-AS1: SubjectSweepCursor.
- [x] D Info: dex and personhood module accounts drop the unused Staking
  permission.

## Skipped, with reason
- A-I4: residual bb assumptions; documentation only (section 9).
- A-I6, B6-6: wallet behaviour (fee rounding, randomized delays).
- C-I1, C-I3, C-I5, I-AS2: inherent or by design.
- C-I4: documented (section 17), no chain change.
- I-D3: LegacyDec precision for extreme pools; changing the TWAP type is a
  feature-sized change.
- I-D4: crediting the export gap at the pre-export price is arguably right
  (the price held); moving ObservedAt forward would understate any TWAP
  spanning the gap.

## Blocked: needs the user
- Removing the devnet faucet (earth1s7rgs...) and gas wallet (earth1jtc2z...)
  from networks/genesis/accounts.json, and swapping the genesis validator to
  earth1n6amvkgfrrgy6ulhurewnm0endkgye69fkcapr: the permission system refused
  the account removal and the genesis rebuild. The rebuild also needs the
  ceremony gentx: the committed gentx's delegator is the old validator.
  accounts.json and genesis.json are unchanged and consistent.

## Wallet changes needed (mobile)
- RegistrationBinding: prepend Bytes(chain_id); vectors reg_pinned ->
  148b3513a501b6ff9c02314f355cb83fb544e22b2a9df79552fe49c944424159.
- Stake vote weight rounded down to 3 significant digits.
- One anchor per bundle (already so); gas_limit <= 5 x simulated gas (already).
- HandleEntry.owner available.
