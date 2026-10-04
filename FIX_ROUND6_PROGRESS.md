# Audit round 6: chain fixes

Branch privacy/orchard from c0ad1dd. Reports: audit6-chain-{A,B,C,D}.md
(scratchpad). Fresh-genesis relaunch: no migrations. Feature freeze: fixes
only. Principle: people are private; power and public money are public.

- [x] D6-1 (High) allocation: resyncFromBonded computes the weight as the
  SDK's GetDelegatorBonded sum with the removed delegation left out, whatever
  the validator's status (bondedWeight). Status changes move no tokens or
  shares, so need no resync; slashes resync at EndBlock; a redelegation is
  refused by the sole-delegator guard. Tests:
  app/audit6_groundworks_weight_test.go (same-block create/vote/undelegate,
  jailed -> unbonding -> partial -> unbonded -> full, bonded, redelegation).
