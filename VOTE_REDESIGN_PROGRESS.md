# Private stake voting on concurrent proposals (vote circuit + indexed nullifier tree)

Branch privacy/orchard. Circuits in wt/mobile-orch/circuits (privacy/orchard).

Problem: MsgStakeVote spent and re-minted the note, so one note could vote on
only one of several open proposals (decoy-proposal attack).

## Steps
- [x] circuits: privacy_core nf_leaf/vote_nf/assert_not_in_indexed; circuits/vote (9,046 gates, 22 tests) — mobile acb398a
- [ ] zk/indexed: Go indexed tree + privacy.NFLeaf/VoteNF; parity vectors with Noir
- [ ] x/shieldedstaking: nullifier set -> indexed tree (value index, index->value, nodes, size, latest root at end block); snapshot nf root/size
- [ ] MsgStakeVote v2 (vote proof, vnf; no spend, no re-mint); gas; sighash
- [ ] x/shielded CircuitVote; privacy-vks; genesis
- [ ] genesis export/import (insertion order, vote nullifiers, snapshot nf roots checked)
- [ ] app tests with real proofs; fixtures
- [ ] docs: ORCHARD_DESIGN §15, CHANGELOG (wallet format, indexer)
