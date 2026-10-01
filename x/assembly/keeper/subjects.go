package keeper

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"

	"github.com/earth-network/earth/x/assembly/types"
	"github.com/earth-network/earth/x/pki/certs"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	"github.com/earth-network/earth/zk/privacy"
)

// A proposal's subjects.
//
// A registration is only as good as the signer behind it, and a signer (or a
// country's signing root) is revoked because its registrations are not
// believed to be distinct humans — a stolen key, or a state minting
// identities. Those registrations are the subject of the vote, so they do not
// get one. Otherwise a compromised signer that registered enough people could
// vote down its own revocation, and the chamber's two-thirds bar would protect
// exactly the registrations it should be able to remove.
//
// One membership proof excludes one Document Signer (excluded_dsc) and one
// country (excluded_country, the leaf's issuing country). So:
//
//   - no revocation: nothing excluded;
//   - exactly one MsgRevokeDsc and nothing else: that DSC excluded (the rest
//     of its country still votes);
//   - otherwise every revocation — each MsgRevokeDsc's issuing country, each
//     MsgRevokeCsca's country — must name one and the same known country,
//     which is excluded as a whole;
//   - anything else (revocations in two countries, or one of unknown
//     country among several) cannot be voted on at all: the chamber refuses
//     every vote with ErrTooManySubjects, the proposal fails for want of
//     votes, and it must be resubmitted split per country (and an
//     unknown-country DSC on its own).
//
// A DSC no trust-store CSCA can have issued has no registrations, so among
// several revocations it constrains nothing. A CSCA revocation of unknown
// country is refused: MsgRevokeCsca is prospective, but the registrations
// already made under it vote until they lapse, and there is no way to
// exclude them as a group.
//
// The country of a revocation comes from x/pki (DscIssuerCountry,
// CscaKeyCountry), which answers only when every certificate that could be
// behind it names the same country; otherwise it is unknown. That is what
// makes excluding by country sound: a registration's leaf commits to the
// country of the CSCA that verified it, which is one of those certificates.
//
// The classification verifies signatures and may walk the trust store, so it
// runs once, as the proposal enters voting (GovHooks), and is stored; every
// vote then reads it. A proposal without a stored classification (imported
// mid-vote) is classified on InitGenesis.

// revocation is one revocation message of a proposal, as the chamber sees it.
type revocation struct {
	dsc     []byte // DSC commitment; nil for a CSCA revocation
	country string // "" unknown
	placed  bool   // false: no trust-store CSCA can have issued this DSC
}

// revocations reads a proposal's top-level MsgRevokeDsc and MsgRevokeCsca.
// Read straight from the messages rather than from a decoded []sdk.Msg: a
// proposal loaded from the store has not had its Any values unpacked. Only the
// top level counts; that is where governance puts them, and nothing else signs
// as the gov authority. A certificate that does not parse revokes nothing
// when the proposal executes either, so it is no subject.
func (k Keeper) revocations(ctx context.Context, proposal v1.Proposal) ([]revocation, error) {
	revokeDsc := sdk.MsgTypeURL(&pkitypes.MsgRevokeDsc{})
	revokeCsca := sdk.MsgTypeURL(&pkitypes.MsgRevokeCsca{})
	var out []revocation
	seen := map[string]bool{}
	for _, m := range proposal.Messages {
		if m == nil {
			continue
		}
		switch m.TypeUrl {
		case revokeDsc:
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
			if seen[string(b[:])] {
				continue
			}
			seen[string(b[:])] = true
			r := revocation{dsc: append([]byte(nil), b[:]...), placed: true}
			if k.pki != nil {
				if r.country, r.placed, err = k.pki.DscIssuerCountry(ctx, msg.CertificateDer); err != nil {
					return nil, err
				}
			}
			out = append(out, r)
		case revokeCsca:
			var msg pkitypes.MsgRevokeCsca
			if err := msg.Unmarshal(m.Value); err != nil {
				continue
			}
			if _, err := certs.ParseCert(msg.CertificateDer); err != nil {
				continue
			}
			r := revocation{placed: true}
			if k.pki != nil {
				country, err := k.pki.CscaKeyCountry(ctx, msg.CertificateDer)
				if err != nil {
					return nil, err
				}
				r.country = country
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// classify turns a proposal's revocations into its subjects.
func classify(revs []revocation) types.ProposalSubjects {
	if len(revs) == 0 {
		return types.ProposalSubjects{}
	}
	if len(revs) == 1 && revs[0].dsc != nil {
		return types.ProposalSubjects{ExcludedDsc: revs[0].dsc}
	}
	countries := map[string]bool{}
	for _, r := range revs {
		if !r.placed {
			continue
		}
		if cf := privacy.CountryField(r.country); cf.IsZero() {
			return types.ProposalSubjects{Refusal: "a revocation of unknown country among several; revoke it in a proposal of its own"}
		}
		countries[r.country] = true
	}
	switch len(countries) {
	case 0:
		// Several DSCs nobody can have registered under: no subjects.
		return types.ProposalSubjects{}
	case 1:
		for c := range countries {
			return types.ProposalSubjects{ExcludedCountry: c}
		}
	}
	names := make([]string, 0, len(countries))
	for c := range countries {
		names = append(names, c)
	}
	sort.Strings(names)
	return types.ProposalSubjects{Refusal: fmt.Sprintf("revocations span countries %s; split the proposal per country", strings.Join(names, ", "))}
}

// classifyProposal computes and stores a proposal's subjects, unless stored.
func (k Keeper) classifyProposal(ctx context.Context, proposal v1.Proposal) error {
	if ok, err := k.Subjects.Has(ctx, proposal.Id); err != nil || ok {
		return err
	}
	revs, err := k.revocations(ctx, proposal)
	if err != nil {
		return err
	}
	return k.Subjects.Set(ctx, proposal.Id, classify(revs))
}

// subjectsOf is a proposal's stored subjects, classifying it now when it has
// none stored (only a proposal that entered voting before this module saw it).
func (k Keeper) subjectsOf(ctx context.Context, proposal v1.Proposal) (types.ProposalSubjects, error) {
	s, err := k.Subjects.Get(ctx, proposal.Id)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return s, err
	}
	revs, err := k.revocations(ctx, proposal)
	if err != nil {
		return s, err
	}
	return classify(revs), nil
}

// forgetSubjects drops a proposal's subjects once its voting is over.
func (k Keeper) forgetSubjects(ctx context.Context, proposalID uint64) error {
	return k.Subjects.Remove(ctx, proposalID)
}

// exclusions is the excluded_dsc and excluded_country a vote on proposal
// proves against, or ErrTooManySubjects.
func (k Keeper) exclusions(ctx context.Context, proposal v1.Proposal) (fr.Element, fr.Element, error) {
	s, err := k.subjectsOf(ctx, proposal)
	if err != nil {
		return fr.Element{}, fr.Element{}, err
	}
	if s.Refusal != "" {
		return fr.Element{}, fr.Element{}, errorsmod.Wrapf(types.ErrTooManySubjects, "proposal %d: %s", proposal.Id, s.Refusal)
	}
	var dsc fr.Element
	if len(s.ExcludedDsc) > 0 {
		if dsc, err = privacy.FieldFromBytes(s.ExcludedDsc); err != nil {
			return fr.Element{}, fr.Element{}, err
		}
	}
	return dsc, privacy.CountryField(s.ExcludedCountry), nil
}

// GovHooks fixes a proposal's subjects as it enters voting. x/gov activates
// voting inside AddDeposit and calls AfterProposalDeposit right after, for the
// initial deposit and every later one.
type GovHooks struct{ k Keeper }

var _ govtypes.GovHooks = GovHooks{}

// GovHooks returns the hooks to register with x/gov.
func (k Keeper) GovHooks() GovHooks { return GovHooks{k: k} }

// AfterProposalDeposit classifies a proposal that is now in voting. An error
// fails the deposit: a proposal must not reach voting without its subjects
// fixed, or a later change to the trust store could move them mid-vote.
func (h GovHooks) AfterProposalDeposit(ctx context.Context, proposalID uint64, _ sdk.AccAddress) error {
	p, err := h.k.gov.Proposals.Get(ctx, proposalID)
	if err != nil {
		return err
	}
	if p.Status != v1.StatusVotingPeriod {
		return nil
	}
	return h.k.classifyProposal(ctx, p)
}

func (GovHooks) AfterProposalSubmission(context.Context, uint64) error           { return nil }
func (GovHooks) AfterProposalVote(context.Context, uint64, sdk.AccAddress) error { return nil }
func (GovHooks) AfterProposalFailedMinDeposit(context.Context, uint64) error     { return nil }
func (GovHooks) AfterProposalVotingPeriodEnded(context.Context, uint64) error    { return nil }
