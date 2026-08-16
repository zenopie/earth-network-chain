package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Hooks keeps withheld commission in step with what happens to the stake that
// earned it. Everything else is a no-op: this module tracks issuance, not
// delegation.
type Hooks struct {
	k Keeper
}

var _ stakingtypes.StakingHooks = Hooks{}

// Hooks returns the staking hooks for the earth module.
func (k Keeper) Hooks() Hooks { return Hooks{k: k} }

// BeforeValidatorSlashed scales down withheld commission by the same fraction
// the validator's stake is losing.
//
// Without it, commission would be the one pot of an operator's earnings that
// misbehaviour cannot touch. A slash already falls on the whole delegation pool,
// so an operator with a thin self-bond pushes most of the cost onto their
// delegators; exempting their commission on top would make it cheaper still.
func (h Hooks) BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, fraction math.LegacyDec) error {
	return h.k.SlashAccruedCommission(ctx, valAddr.Bytes(), fraction)
}

// AfterValidatorRemoved destroys commission still withheld for a validator whose
// record is being deleted.
//
// Tombstoning alone does not need this — a jailed or tombstoned validator can
// still be delegated to, so its operator compounds normally and then unbonds
// like anyone else, with no shortcut to liquidity. But once the record is gone
// there is nothing left to delegate into, and CompoundCommission would fail
// forever, stranding coins on the module account against a ledger nobody can
// drain.
func (h Hooks) AfterValidatorRemoved(ctx context.Context, _ sdk.ConsAddress, valAddr sdk.ValAddress) error {
	return h.k.ForfeitAccruedCommission(ctx, valAddr.Bytes())
}

// --- remaining hooks are no-ops ---

func (h Hooks) AfterValidatorCreated(context.Context, sdk.ValAddress) error   { return nil }
func (h Hooks) BeforeValidatorModified(context.Context, sdk.ValAddress) error { return nil }
func (h Hooks) AfterValidatorBonded(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterValidatorBeginUnbonding(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationCreated(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationSharesModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterDelegationModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationRemoved(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
