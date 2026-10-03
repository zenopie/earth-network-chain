# Private stake voting on concurrent proposals (vote circuit + indexed nullifier tree)

Branch privacy/orchard. Circuits in wt/mobile-orch/circuits (privacy/orchard).

Problem: MsgStakeVote spent and re-minted the note, so one note could vote on
only one of several open proposals (decoy-proposal attack).

## Steps
- [x] circuits: privacy_core nf_leaf/vote_nf/assert_not_in_indexed; circuits/vote (9,046 gates, 22 tests) — mobile acb398a
- [x] zk/indexed: Go indexed tree + privacy.NFLeaf/VoteNF; parity vectors with Noir (mobile: test_go_parity)
- [x] x/shieldedstaking: nullifier set -> indexed tree (value index, index->value, nodes, size, latest root at end block); snapshot nf root/size
- [x] MsgStakeVote v2 (vote proof, vnf; no spend, no re-mint); gas; sighash
- [x] x/shielded CircuitVote; privacy-vks; genesis
- [x] genesis export/import (insertion order, vote nullifiers, snapshot nf roots checked)
- [x] app tests with real proofs; fixtures (TestStakeVoteConcurrentProposals, TestStakeVoteTally reworked; staking fixtures re-recorded, 145)
- [ ] docs: ORCHARD_DESIGN §15, CHANGELOG (wallet format, indexer)

