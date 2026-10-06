package types

import (
	"context"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	allocationtypes "github.com/earth-network/earth/x/allocation/types"
	"github.com/earth-network/earth/x/pki/certs"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// AllocationKeeper is the slice of x/allocation this module needs: the
// caretaker stream's voters, which this module files under anonymous
// caretaker nullifiers, and the registration-reward pool it draws down.
type AllocationKeeper interface {
	// AdvanceIndex settles a stream up to the current block. Required before
	// clearing a voter, so the weight being removed is credited against a current
	// index rather than silently forfeiting this block's emission.
	AdvanceIndex(ctx context.Context, stream allocationtypes.StreamId) error
	// AdvanceIndexTo settles a stream up to unix time t (no later than the
	// block time; a no-op at or before its last settlement).
	AdvanceIndexTo(ctx context.Context, stream allocationtypes.StreamId, t int64) error
	// ValidateSplit checks a split against the stream's options as they stand
	// (sum 100, no duplicates, live options, at most MaxVoterOptions).
	ValidateSplit(ctx context.Context, stream allocationtypes.StreamId, percentages []allocationtypes.AllocationWeight) error
	// SetVoterSplit validates a split against the stream's options, settles
	// the stream and files the split under voter at weight (clearing it when
	// percentages is empty).
	SetVoterSplit(ctx context.Context, stream allocationtypes.StreamId, voter []byte, percentages []allocationtypes.AllocationWeight, weight math.Int) error
	// ClearVoter retires a voter's split in a stream, returning its weight to
	// the stream. Called when a caretaker split lapses.
	ClearVoter(ctx context.Context, stream allocationtypes.StreamId, voter []byte) error
	// MoveVoter files from's split under to and clears from (a caretaker
	// move), settling the stream first.
	MoveVoter(ctx context.Context, stream allocationtypes.StreamId, from, to []byte) error
	// DrawFromOption settles an option and withdraws `ppm` parts-per-million of
	// its accrued ERTH for the caller to pay out.
	DrawFromOption(ctx context.Context, stream allocationtypes.StreamId, optionID uint64, ppm int64) (math.Int, error)
	// PayOutToModule sends drawn ERTH from the allocation module account to a
	// module account; this module moves it on into the shielded pool.
	PayOutToModule(ctx context.Context, recipientModule string, amount math.Int) error
}

// ShieldedKeeper is the slice of x/shielded this module needs: minting notes
// to hidden owners, running its private msgs through the private ante, and
// verifying membership proofs against the pool's verifying keys.
type ShieldedKeeper interface {
	MintNote(ctx context.Context, fromModule string, coin sdk.Coin, pc, ciphertext []byte) (uint64, []byte, error)
	// MintOpenNote mints a note to a public owner_pk with a chain-chosen
	// opening, emitted on the mint event (the referral note).
	MintOpenNote(ctx context.Context, fromModule string, coin sdk.Coin, ownerPK, rho, rcm fr.Element) (uint64, []byte, error)
	RegisterPrivateAction(msgTypeURL string, h shieldedtypes.PrivateActionHandler)
	VerifyCircuit(ctx context.Context, circuit string, proof []byte, publicInputs [][]byte) error
	PrivateGasPrices(ctx context.Context) (proof, note uint64, err error)
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
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
	// blocked list ("not allowed to receive funds").
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
	// VerifyDscIssuer checks that a DER-encoded Document Signer certificate chains to
	// a trusted CSCA, is currently valid, and has not been revoked. It returns
	// the DSC's parsed public key, from which this module recomputes the
	// commitment the register circuit exposes as a public input.
	//
	// The parsed key rather than its canonical bytes, because the commitment is
	// over a curve tag as well as the coordinates and the bytes cannot say which
	// curve produced them — see certs.DscCommitment.
	//
	// Also returns the issuing country, taken from the trusted CSCA that
	// verified it rather than from the certificate itself, which could name
	// any country — or none.
	VerifyDscIssuer(ctx context.Context, der []byte) (*certs.PublicKey, string, error)
}

// BurnRecorder is x/earth's cumulative burn counters, narrowed to the one call
// this module makes into them. Burns are unobservable after the fact — x/bank
// records only the supply that remains — so every burn here is counted as it
// happens. See x/earth/keeper/burns.go.
type BurnRecorder interface {
	RecordBurn(ctx context.Context, source string, coins sdk.Coins) error
}
