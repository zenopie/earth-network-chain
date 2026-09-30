package keeper

import (
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/earth-network/earth/x/assembly/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	"github.com/earth-network/earth/zk/privacy"
)

// revokedSigners returns the Document Signer commitments a proposal revokes,
// keyed by their bytes. Empty for any proposal that revokes none.
//
// A registration is only as good as the signer behind it, and a signer is
// revoked because its registrations are not believed to be distinct humans —
// a stolen key, or a state minting identities. Those registrations are the
// subject of the vote, so they do not get one (see excludedDsc). Otherwise a compromised signer
// that registered enough people could vote down its own revocation, and the
// chamber's two-thirds bar would protect exactly the registrations it should
// be able to remove.
//
// Read straight from the proposal's messages rather than from a decoded
// []sdk.Msg: a proposal loaded from the store has not had its Any values
// unpacked, and only the one message type matters here. A certificate that
// does not parse revokes nothing when the proposal executes either, so it
// excludes no one.
//
// Only a MsgRevokeDsc at the top level of the proposal counts. That is where
// governance puts it; the gov authority is its required signer, and nothing
// else signs as the gov authority.
func revokedSigners(proposal v1.Proposal) map[string]struct{} {
	subjects := map[string]struct{}{}
	revokeURL := sdk.MsgTypeURL(&pkitypes.MsgRevokeDsc{})
	for _, m := range proposal.Messages {
		if m == nil || m.TypeUrl != revokeURL {
			continue
		}
		var msg pkitypes.MsgRevokeDsc
		if err := msg.Unmarshal(m.Value); err != nil {
			continue
		}
		cert, err := certs.ParseCert(msg.CertificateDer)
		if err != nil {
			continue
		}
		c, err := certs.DscCommitmentOf(cert.PublicKey)
		if err != nil {
			continue
		}
		b := c.Bytes()
		subjects[string(b[:])] = struct{}{}
	}
	return subjects
}

// excludedDsc is the excluded_dsc a vote on proposal proves against: the one
// Document Signer the proposal revokes, or 0. The membership proof shows the
// voter's leaf was not made under it, which is what keeps a revoked signer's
// registrations from voting down their own revocation. A proposal revoking
// several signers cannot be voted on (one exclusion per proof).
func excludedDsc(proposal v1.Proposal) (fr.Element, error) {
	subjects := revokedSigners(proposal)
	switch len(subjects) {
	case 0:
		return fr.Element{}, nil
	case 1:
		for s := range subjects {
			return privacy.FieldFromBytes([]byte(s))
		}
	}
	return fr.Element{}, errorsmod.Wrapf(types.ErrTooManySubjects, "proposal %d revokes %d", proposal.Id, len(subjects))
}
