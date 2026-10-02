# Orchard Phase 2 progress (chain privacy/orchard)

Checklist (from the Phase 2 brief): personhood, assembly, shieldedstaking,
dex on bundles; cleanup of legacy transfer code; gas-check; plan file.

## Done (committed)
- (none yet)

## In progress
- step 1: x/shielded/testutil generic bundle builder + fee-bundle helpers

## Next
- personhood + assembly (proto, msgs, keeper, app tests, fixtures)

## Decisions
- Fee-only msgs (personhood, assembly) carry `Bundle fee = 1`; their fee is
  the bundle's uerth balance, which must be its only balance (nothing else is
  released). No separate fee field: the digest already binds the balance.
