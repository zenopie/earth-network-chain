package keeper

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/earth-network/earth/x/personhood/types"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Audit 3 L6: binding a referral address takes its owner's signed consent
// for this nullifier on this chain; nobody squats an address they do not
// control.
func TestAudit3ReferrerConsent(t *testing.T) {
	owner := secp256k1.GenPrivKeyFromSecret([]byte("referrer/owner"))
	squatter := secp256k1.GenPrivKeyFromSecret([]byte("referrer/squatter"))
	addr := sdk.AccAddress(owner.PubKey().Address())
	nf := privacy.FieldBytes(privacy.U64(4242))
	const chain = "earth-1"
	const height, expiry = int64(1000), uint64(1100)
	sign := func(k *secp256k1.PrivKey, chainID string, nf, a []byte) []byte {
		sig, err := k.Sign(types.ReferrerConsentBytes(chainID, nf, expiry, a))
		require.NoError(t, err)
		return sig
	}
	msg := func(pub, sig []byte) *types.MsgBindReferrer {
		return &types.MsgBindReferrer{Address: addr.String(), Membership: types.Membership{Nullifier: nf, Root: nf, Proof: make([]byte, shieldedtypes.ProofBytes)},
			ReferrerPubKey: pub, ReferrerSignature: sig, ConsentExpiryHeight: expiry}
	}

	ok := msg(owner.PubKey().Bytes(), sign(owner, chain, nf, addr))
	require.NoError(t, checkReferrerConsent(chain, height, ok, addr))
	require.NoError(t, checkReferrerConsent(chain, int64(expiry), ok, addr), "valid at its expiry height")

	for name, m := range map[string]*types.MsgBindReferrer{
		"no consent":           msg(nil, nil),
		"squatter's key":       msg(squatter.PubKey().Bytes(), sign(squatter, chain, nf, addr)),
		"owner key, squat sig": msg(owner.PubKey().Bytes(), sign(squatter, chain, nf, addr)),
		"other nullifier":      msg(owner.PubKey().Bytes(), sign(owner, chain, privacy.FieldBytes(privacy.U64(1)), addr)),
		"other chain":          msg(owner.PubKey().Bytes(), sign(owner, "earth-2", nf, addr)),
		"other address signed": msg(owner.PubKey().Bytes(), sign(owner, chain, nf, sdk.AccAddress(squatter.PubKey().Address()))),
	} {
		require.ErrorIs(t, checkReferrerConsent(chain, height, m, addr), types.ErrNoReferrerConsent, name)
	}

	// ValidateBasic: a bind needs both consent fields at their sizes; a clear
	// carries none.
	require.ErrorIs(t, msg(nil, nil).ValidateBasic(), types.ErrNoReferrerConsent)
	clear := msg(owner.PubKey().Bytes(), nil)
	clear.Address = ""
	require.ErrorContains(t, clear.ValidateBasic(), "carries no consent")
}

// Audit 4 C10: the consent names an expiry height. Past it the consent is
// refused (a signature given once cannot rebind the address later); an
// expiry more than ReferrerConsentMaxBlocks ahead is refused; and the
// signature covers the height, so it cannot be moved.
func TestAudit4ReferrerConsentExpires(t *testing.T) {
	owner := secp256k1.GenPrivKeyFromSecret([]byte("referrer/owner"))
	addr := sdk.AccAddress(owner.PubKey().Address())
	nf := privacy.FieldBytes(privacy.U64(4242))
	const chain = "earth-1"
	bind := func(signed, claimed uint64) *types.MsgBindReferrer {
		sig, err := owner.Sign(types.ReferrerConsentBytes(chain, nf, signed, addr))
		require.NoError(t, err)
		return &types.MsgBindReferrer{Address: addr.String(), Membership: types.Membership{Nullifier: nf},
			ReferrerPubKey: owner.PubKey().Bytes(), ReferrerSignature: sig, ConsentExpiryHeight: claimed}
	}
	require.NoError(t, checkReferrerConsent(chain, 500, bind(600, 600), addr))
	require.ErrorContains(t, checkReferrerConsent(chain, 601, bind(600, 600), addr), "expired")
	require.ErrorIs(t, checkReferrerConsent(chain, 500, bind(500+types.ReferrerConsentMaxBlocks+1, 500+types.ReferrerConsentMaxBlocks+1), addr),
		types.ErrNoReferrerConsent)
	require.NoError(t, checkReferrerConsent(chain, 500, bind(500+types.ReferrerConsentMaxBlocks, 500+types.ReferrerConsentMaxBlocks), addr))
	require.ErrorContains(t, checkReferrerConsent(chain, 500, bind(600, 700), addr), "does not verify", "the height is signed")

	noExpiry := bind(600, 0)
	noExpiry.Membership = types.Membership{Nullifier: nf, Root: nf, Proof: make([]byte, shieldedtypes.ProofBytes)}
	require.ErrorIs(t, noExpiry.ValidateBasic(), types.ErrNoReferrerConsent)
}
