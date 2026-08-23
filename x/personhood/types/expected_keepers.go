package types

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
)

// AllocationKeeper is the slice of x/allocation this module needs. The human
// emission stream lives there; what lives here is the two things only
// proof-of-personhood can answer — when a vote stops being backed by a live
// human, and when the registration-reward pool should be paid out.
type AllocationKeeper interface {
	// AdvanceIndex settles a stream up to the current block. Required before
	// clearing a voter, so the weight being removed is credited against a current
	// index rather than silently forfeiting this block's emission.
	AdvanceIndex(ctx context.Context, stream allocationtypes.StreamId) error
	// ClearVoter retires an address's vote in a stream, returning its weight to
	// the stream. Called when a registration lapses or is replaced.
	ClearVoter(ctx context.Context, stream allocationtypes.StreamId, addr []byte) error
	// DrawFromOption settles an option and withdraws `ppm` parts-per-million of
	// its accrued ERTH for the caller to pay out.
	DrawFromOption(ctx context.Context, stream allocationtypes.StreamId, optionID uint64, ppm int64) (math.Int, error)
	// PayOut sends drawn ERTH from the allocation module account. The coins
	// already exist — x/allocation mints the emission as it accrues — so this
	// module never issues allocation ERTH itself.
	PayOut(ctx context.Context, recipient sdk.AccAddress, amount math.Int) error
}

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	GetSupply(ctx context.Context, denom string) sdk.Coin
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// DexKeeper defines the expected interface for the Dex module. Used to resolve
// the ERTH (hub) denom and to swap ERTH for ANML during the buyback-and-burn.
type DexKeeper interface {
	HubDenom(ctx context.Context) (string, error)
	HasPoolForToken(ctx context.Context, tokenDenom string) (bool, error)
	// SwapExactInForModule trades on behalf of a module account. The ordinary
	// SwapExactIn cannot be used here: it pays out with
	// SendCoinsFromModuleToAccount, and this module's account is on the bank's
	// blocked list, so every buyback failed with "not allowed to receive funds"
	// and was silently discarded.
	SwapExactInForModule(ctx context.Context, moduleName string, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error)
	// TwapObservation reads the pool's time-weighted price accumulator and its
	// current spot price. The buyback stores one reading, takes another a window
	// later, and prices against the average between them instead of against
	// whatever the last trade left behind.
	TwapObservation(ctx context.Context, tokenDenom string) (cumulative, spot math.LegacyDec, observedAt int64, err error)
	// QuoteHubToToken reports what an ERTH -> token swap would return right now
	// without executing it, so the buyback can set a min_out that already
	// accounts for its own price impact against the current depth.
	QuoteHubToToken(ctx context.Context, tokenDenom string, amountErthIn math.Int) (math.Int, error)
}

// PkiKeeper defines the expected interface for the x/pki module: it owns the
// CSCA trust store and decides whether a given Document Signer certificate is
// trustworthy, so a registration proof can be bound to a specific, verified
// signer.
type PkiKeeper interface {
	// VerifyDsc checks that a DER-encoded Document Signer certificate chains to
	// a trusted CSCA, is currently valid, and has not been revoked. It returns
	// the DSC's canonical public-key bytes (ECDSA: x‖y, RSA: modulus
	// big-endian), from which this module recomputes the commitment the
	// register circuit exposes as a public input.
	VerifyDsc(ctx context.Context, der []byte) ([]byte, error)
	// IsCommitmentRevoked answers the same question for a signer this module has
	// already recorded, which knows it only by the Poseidon2 commitment stored
	// on the Registration — not by the certificate VerifyDsc takes.
	IsCommitmentRevoked(ctx context.Context, commitment []byte) (bool, error)
}
