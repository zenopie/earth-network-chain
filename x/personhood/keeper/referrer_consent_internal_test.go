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
	sign := func(k *secp256k1.PrivKey, chainID string, nf, a []byte) []byte {
		sig, err := k.Sign(types.ReferrerConsentBytes(chainID, nf, a))
		require.NoError(t, err)
		return sig
	}
	msg := func(pub, sig []byte) *types.MsgBindReferrer {
		return &types.MsgBindReferrer{Address: addr.String(), Membership: types.Membership{Nullifier: nf, Root: nf, Proof: make([]byte, shieldedtypes.ProofBytes)},
			ReferrerPubKey: pub, ReferrerSignature: sig}
	}

	ok := msg(owner.PubKey().Bytes(), sign(owner, chain, nf, addr))
	require.NoError(t, checkReferrerConsent(chain, ok, addr))

	for name, m := range map[string]*types.MsgBindReferrer{
		"no consent":           msg(nil, nil),
		"squatter's key":       msg(squatter.PubKey().Bytes(), sign(squatter, chain, nf, addr)),
		"owner key, squat sig": msg(owner.PubKey().Bytes(), sign(squatter, chain, nf, addr)),
		"other nullifier":      msg(owner.PubKey().Bytes(), sign(owner, chain, privacy.FieldBytes(privacy.U64(1)), addr)),
		"other chain":          msg(owner.PubKey().Bytes(), sign(owner, "earth-2", nf, addr)),
		"other address signed": msg(owner.PubKey().Bytes(), sign(owner, chain, nf, sdk.AccAddress(squatter.PubKey().Address()))),
	} {
		require.ErrorIs(t, checkReferrerConsent(chain, m, addr), types.ErrNoReferrerConsent, name)
	}

	// ValidateBasic: a bind needs both consent fields at their sizes; a clear
	// carries none.
	require.ErrorIs(t, msg(nil, nil).ValidateBasic(), types.ErrNoReferrerConsent)
	clear := msg(owner.PubKey().Bytes(), nil)
	clear.Address = ""
	require.ErrorContains(t, clear.ValidateBasic(), "carries no consent")
}
