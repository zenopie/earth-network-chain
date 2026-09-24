package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
)

// revokedSigners returns the Document Signer commitments a proposal revokes,
// keyed by their bytes. Empty for any proposal that revokes none.
//
// A registration is only as good as the signer behind it, and a signer is
// revoked because its registrations are not believed to be distinct humans —
// a stolen key, or a state minting identities. Those registrations are the
// subject of the vote, so they do not get one. Otherwise a compromised signer
// that registered enough people could vote down its own revocation, and the
// chamber's two-thirds bar would protect exactly the registrations it should
// be able to remove.
//
// A proposal's messages are fixed when it is submitted, so refusing the vote
// when it is cast is the whole of it: no registration can come to be a subject
// after voting.
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

// isSubject reports whether the registration filed under nullifier was made
// under one of the signers in subjects. Checked when a vote is cast, so such a
// vote never reaches the tally.
func (k Keeper) isSubject(ctx context.Context, subjects map[string]struct{}, nullifier []byte) (bool, error) {
	if len(subjects) == 0 {
		return false, nil
	}
	dsc, err := k.personhood.RegistrationDsc(ctx, nullifier)
	if err != nil || len(dsc) == 0 {
		return false, err
	}
	_, ok := subjects[string(dsc)]
	return ok, nil
}
