package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/zk/orchard"
	"github.com/earth-network/earth/zk/privacy"
)

// CvBytes is an action's value commitment encoding: x || y.
const CvBytes = 64

func checkField(what string, b []byte) error {
	if _, err := privacy.FieldFromBytes(b); err != nil {
		return errorsmod.Wrapf(ErrInvalidBundle, "%s: %v", what, err)
	}
	return nil
}

// ValidateBasic checks everything about a bundle that needs no state:
// MinActionsPerBundle..orchard.MaxActions actions (params may cap lower),
// every field canonical, every cv a Grumpkin point, nullifiers distinct,
// balances positive with distinct, valid denoms, a binding signature of the
// right length.
func (b *Bundle) ValidateBasic() error {
	n := len(b.Actions)
	if n < MinActionsPerBundle || n > orchard.MaxActions {
		return errorsmod.Wrapf(ErrInvalidBundle, "a bundle carries %d..%d actions (pad with dummies), got %d", MinActionsPerBundle, orchard.MaxActions, n)
	}
	seen := make(map[string]bool, n)
	for i := range b.Actions {
		a := &b.Actions[i]
		if err := CheckProofLength(a.Proof); err != nil {
			return errorsmod.Wrapf(ErrInvalidBundle, "action %d: %v", i, err)
		}
		if err := checkField(fmt.Sprintf("action %d anchor", i), a.Anchor); err != nil {
			return err
		}
		if err := checkField(fmt.Sprintf("action %d nullifier", i), a.Nullifier); err != nil {
			return err
		}
		if err := checkField(fmt.Sprintf("action %d commitment", i), a.Commitment); err != nil {
			return err
		}
		if len(a.Cv) != CvBytes {
			return errorsmod.Wrapf(ErrInvalidBundle, "action %d: cv must be %d bytes", i, CvBytes)
		}
		if _, err := orchard.PointFromBytes(a.Cv); err != nil {
			return errorsmod.Wrapf(ErrInvalidBundle, "action %d cv: %v", i, err)
		}
		if len(a.Ciphertext) > MaxCiphertextBytes {
			return errorsmod.Wrapf(ErrInvalidBundle, "action %d: ciphertext exceeds %d bytes", i, MaxCiphertextBytes)
		}
		if seen[string(a.Nullifier)] {
			return errorsmod.Wrap(ErrInvalidBundle, "duplicate nullifier")
		}
		seen[string(a.Nullifier)] = true
	}
	// A balance of an asset no action touches has no discrete log the signer
	// could know, so at most two per action (a spend's and an output's) can
	// ever verify; the bound caps the scalar multiplications the binding key
	// costs before the signature is checked.
	if len(b.Balances) > 2*n {
		return errorsmod.Wrapf(ErrInvalidBundle, "at most %d balances for %d actions", 2*n, n)
	}
	denoms := make(map[string]bool, len(b.Balances))
	for _, bal := range b.Balances {
		if err := sdk.ValidateDenom(bal.Denom); err != nil {
			return errorsmod.Wrapf(ErrInvalidBundle, "balance denom: %v", err)
		}
		if bal.Amount == 0 || denoms[bal.Denom] {
			return errorsmod.Wrap(ErrInvalidBundle, "balances must be positive, one per denom")
		}
		denoms[bal.Denom] = true
	}
	if len(b.BindingSig) != orchard.BindingSigSize {
		return errorsmod.Wrapf(ErrInvalidBundle, "binding signature must be %d bytes", orchard.BindingSigSize)
	}
	return nil
}

// ToOrchard is b in zk/orchard's terms, every denom mapped to its asset id
// (zk/privacy.AssetID(denom), which is also what the registry records). Call
// after ValidateBasic.
func (b *Bundle) ToOrchard() (*orchard.Bundle, error) {
	out := &orchard.Bundle{
		Actions:    make([]orchard.Action, len(b.Actions)),
		Balances:   make([]orchard.Balance, len(b.Balances)),
		BindingSig: b.BindingSig,
	}
	for i := range b.Actions {
		a := &b.Actions[i]
		var err error
		o := &out.Actions[i]
		if o.Anchor, err = privacy.FieldFromBytes(a.Anchor); err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidBundle, "action %d anchor: %v", i, err)
		}
		if o.Nullifier, err = privacy.FieldFromBytes(a.Nullifier); err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidBundle, "action %d nullifier: %v", i, err)
		}
		if o.Commitment, err = privacy.FieldFromBytes(a.Commitment); err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidBundle, "action %d commitment: %v", i, err)
		}
		if o.Cv, err = orchard.PointFromBytes(a.Cv); err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidBundle, "action %d cv: %v", i, err)
		}
		o.Ciphertext, o.Proof = a.Ciphertext, a.Proof
	}
	for i, bal := range b.Balances {
		out.Balances[i] = orchard.Balance{Asset: privacy.AssetID(bal.Denom), Value: bal.Amount}
	}
	return out, nil
}

// Digest is zk/orchard's bundle digest of b. Call after ValidateBasic.
func (b *Bundle) Digest() (fr.Element, error) {
	o, err := b.ToOrchard()
	if err != nil {
		return fr.Element{}, err
	}
	return o.Digest(), nil
}

// Nullifiers is every action's nullifier, in order.
func (b *Bundle) Nullifiers() [][]byte {
	out := make([][]byte, len(b.Actions))
	for i := range b.Actions {
		out[i] = b.Actions[i].Nullifier
	}
	return out
}

// Balance is b's balance of denom, 0 if it has none.
func (b *Bundle) Balance(denom string) uint64 {
	for _, bal := range b.Balances {
		if bal.Denom == denom {
			return bal.Amount
		}
	}
	return 0
}
