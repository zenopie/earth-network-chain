package types

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

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
	SwapExactIn(ctx context.Context, trader sdk.AccAddress, tokenIn sdk.Coin, denomOut string, minOut math.Int) (sdk.Coin, error)
}

// PkiKeeper defines the expected interface for the x/pki module: it owns the
// CSCA trust store and decides whether a given Document Signer certificate is
// trustworthy, so a registration proof can be bound to a specific, verified
// signer.
type PkiKeeper interface {
	// VerifyDsc checks that a DER-encoded Document Signer certificate chains to
	// a trusted CSCA, is currently valid, and has not been revoked. It returns
	// the DSC's canonical public-key bytes (ECDSA: x‖y, RSA: modulus
	// big-endian), from which the caretaker recomputes the commitment the
	// register circuit exposes as a public input.
	VerifyDsc(ctx context.Context, der []byte) ([]byte, error)
}
