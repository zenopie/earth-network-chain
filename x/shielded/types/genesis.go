package types

import (
	"bytes"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// DefaultAssets are the denoms admitted at genesis: ERTH (also the fee asset)
// and ANML (which exists only here).
func DefaultAssets() []Asset {
	return []Asset{NewAsset(FeeDenom), NewAsset(AnmlDenom)}
}

// NewAsset derives denom's registry entry.
func NewAsset(denom string) Asset {
	return Asset{Denom: denom, AssetId: privacy.FieldBytes(privacy.AssetID(denom))}
}

// DefaultGenesis returns the default genesis state: an empty pool admitting
// uerth and uanml.
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params: DefaultParams(),
		Assets: DefaultAssets(),
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return err
	}
	denoms := map[string]bool{}
	for _, a := range gs.Assets {
		if err := sdk.ValidateDenom(a.Denom); err != nil {
			return fmt.Errorf("asset %q: %w", a.Denom, err)
		}
		if denoms[a.Denom] {
			return fmt.Errorf("duplicate asset %q", a.Denom)
		}
		denoms[a.Denom] = true
		if !bytes.Equal(a.AssetId, NewAsset(a.Denom).AssetId) {
			return fmt.Errorf("asset %q: asset_id is not AssetID(denom)", a.Denom)
		}
	}
	if !denoms[FeeDenom] {
		return fmt.Errorf("the fee asset %s must be admitted", FeeDenom)
	}
	if uint64(len(gs.Commitments)) > merkle.Capacity {
		return fmt.Errorf("%d commitments exceed the tree's capacity", len(gs.Commitments))
	}
	for i, cm := range gs.Commitments {
		if _, err := privacy.FieldFromBytes(cm); err != nil {
			return fmt.Errorf("commitment %d: %w", i, err)
		}
	}
	seen := map[string]bool{}
	for i, nf := range gs.Nullifiers {
		if _, err := privacy.FieldFromBytes(nf); err != nil {
			return fmt.Errorf("nullifier %d: %w", i, err)
		}
		if seen[string(nf)] {
			return fmt.Errorf("duplicate nullifier %X", nf)
		}
		seen[string(nf)] = true
	}
	roots := map[string]bool{}
	for i, r := range gs.Roots {
		if _, err := privacy.FieldFromBytes(r.Root); err != nil {
			return fmt.Errorf("root %d: %w", i, err)
		}
		if roots[string(r.Root)] {
			return fmt.Errorf("duplicate root %X", r.Root)
		}
		roots[string(r.Root)] = true
		if r.TreeSize > uint64(len(gs.Commitments)) {
			return fmt.Errorf("root %d claims tree_size %d beyond the %d commitments", i, r.TreeSize, len(gs.Commitments))
		}
	}
	turn := map[string]bool{}
	for _, t := range gs.Turnstiles {
		if !denoms[t.Denom] {
			return fmt.Errorf("turnstile for unadmitted denom %q", t.Denom)
		}
		if turn[t.Denom] {
			return fmt.Errorf("duplicate turnstile %q", t.Denom)
		}
		turn[t.Denom] = true
		if t.In.IsNil() || t.Out.IsNil() || t.Out.IsNegative() || t.In.LT(t.Out) {
			return fmt.Errorf("turnstile %q: need 0 <= out <= in", t.Denom)
		}
	}
	return nil
}

// Held is what the turnstile says the pool holds: in - out.
func (t Turnstile) Held() math.Int { return t.In.Sub(t.Out) }
