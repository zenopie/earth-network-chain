package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// CanonicalValoper refuses a validator operator string that is not the
// canonical encoding of its address: bech32 under the chain's validator
// prefix, a 20- or 32-byte address, lowercase, exactly as re-encoding the
// decoded bytes produces it.
//
// This module keys everything by the string (books, derth and unbond denoms,
// unbond records, positions, snapshots, tallies), while x/staking keys by the
// decoded bytes. bech32 decodes an all-uppercase string to the same bytes, so
// without this check "EARTHVALOPER1..." was a second, independent book over
// the same SDK delegation (audit F0): its backing counted the whole module
// delegation while its supply was whatever its attacker minted.
func CanonicalValoper(v string) error {
	if v == "" || len(v) > 128 {
		return errorsmod.Wrap(ErrInvalidMsg, "invalid validator")
	}
	hrp, bz, err := bech32.DecodeAndConvert(v)
	if err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "invalid validator %q: %v", v, err)
	}
	if want := sdk.GetConfig().GetBech32ValidatorAddrPrefix(); hrp != want {
		return errorsmod.Wrapf(ErrInvalidMsg, "validator %q: prefix %q, want %q", v, hrp, want)
	}
	if len(bz) != 20 && len(bz) != 32 {
		return errorsmod.Wrapf(ErrInvalidMsg, "validator %q: %d-byte address", v, len(bz))
	}
	canon, err := bech32.ConvertAndEncode(hrp, bz)
	if err != nil {
		return errorsmod.Wrapf(ErrInvalidMsg, "validator %q: %v", v, err)
	}
	if canon != v {
		return errorsmod.Wrapf(ErrInvalidMsg, "validator %q is not canonical (%s)", v, canon)
	}
	return nil
}
