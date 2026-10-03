# Fix round 3 (chain audit 3) progress

PoCs: /private/tmp/claude-501/audit3/{core/chain,staking/repo,pdx/*}. Each is
ported as a regression test asserting the safe outcome.

- [ ] 1 escrow poisoning (HIGH)
- [ ] 2 unshield into module accounts
- [ ] 3 stake votes on concurrent proposals (no nullifier spend)
- [ ] 4 gas-check blind to revocations
- [ ] 5 demoted expedited proposal loses subjects
- [ ] 6 mempool fork (force NoOpMempool)
- [ ] 7 unbond record growth
- [ ] 8 vote weight canonical strings
- [ ] 9 genesis root records
- [ ] 10 personhood L1/L2/L4/L5/L6/I1
- [ ] 11 dex L3 rounding / L7 buyback cap
- [ ] 12 info: shieldedstaking perms, genesis delegation rule, F5
