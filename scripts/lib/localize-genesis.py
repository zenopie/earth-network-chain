"""Turn the launch genesis into a throwaway local one.

networks/genesis.json is earth-1's, and it is hash-pinned: its hash is the
chain's identity, so it is never edited. This copies it and changes only what a
local network cannot inherit, leaving everything that makes the chain itself —
the CSCA trust store, the verifying keys, the pre-mine, the pool reserves — as
shipped.

    localize-genesis.py <path> <chain-id> [voting-period]
"""

import datetime
import json
import sys


def halve(period: str) -> str:
    """Half of a duration like "25s", "2m" or "1h", floored at five seconds."""
    units = {"s": 1, "m": 60, "h": 3600}
    unit = period[-1]
    if unit not in units:
        raise SystemExit(f"cannot read a duration from {period!r}; use s, m or h")
    seconds = float(period[:-1]) * units[unit]
    return f"{max(5, int(seconds // 2))}s"


def main() -> None:
    if not 3 <= len(sys.argv) <= 4:
        sys.exit(__doc__)
    path, chain_id = sys.argv[1], sys.argv[2]
    voting_period = sys.argv[3] if len(sys.argv) > 3 else ""

    with open(path) as f:
        g = json.load(f)

    # A local network must not share the mainnet chain id: a transaction signed
    # here would otherwise be replayable there.
    g["chain_id"] = chain_id
    # Genesis in the past means the node spends its first minutes replaying the
    # gap before it produces anything.
    g["genesis_time"] = datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )
    # The launch gentx is signed for earth-1 by a key this machine does not hold.
    # collect-gentxs puts a locally signed one in its place.
    g["app_state"]["genutil"]["gen_txs"] = []

    if voting_period:
        gov = g["app_state"]["gov"]["params"]
        gov["voting_period"] = voting_period
        # x/gov refuses a genesis where these two are equal, so the expedited
        # period is derived rather than taken: half, with a floor, which keeps
        # the fast track faster at any value someone passes in.
        gov["expedited_voting_period"] = halve(voting_period)
        gov["max_deposit_period"] = "120s"

    with open(path, "w") as f:
        json.dump(g, f, indent=2)


if __name__ == "__main__":
    main()
