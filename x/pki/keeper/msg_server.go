package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"

	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/x/pki/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns a MsgServer for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

func (k msgServer) checkAuthority(authority string) error {
	addr, err := k.addressCodec.StringToBytes(authority)
	if err != nil {
		return errorsmod.Wrap(err, "invalid authority address")
	}
	if !bytes.Equal(k.GetAuthority(), addr) {
		want, _ := k.addressCodec.BytesToString(k.GetAuthority())
		return errorsmod.Wrapf(types.ErrUnauthorized, "expected %s, got %s", want, authority)
	}
	return nil
}

func (k msgServer) UpdateParams(ctx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	if err := req.Params.Validate(); err != nil {
		return nil, err
	}
	if err := k.Params.Set(ctx, req.Params); err != nil {
		return nil, err
	}
	return &types.MsgUpdateParamsResponse{}, nil
}

func (k msgServer) AddCsca(ctx context.Context, req *types.MsgAddCsca) (*types.MsgAddCscaResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	if err := k.AddCscaDER(ctx, req.CertificateDer); err != nil {
		return nil, err
	}
	return &types.MsgAddCscaResponse{}, nil
}

// RevokeDsc withdraws trust from a Document Signer. Governance-gated: an
// erroneous revocation would lock out every holder whose passport that signer
// issued, so it is not permissionless the way submission is.
func (k msgServer) RevokeDsc(ctx context.Context, req *types.MsgRevokeDsc) (*types.MsgRevokeDscResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	cert, err := certs.ParseCert(req.CertificateDer)
	if err != nil {
		return nil, types.ErrInvalidCert.Wrap(err.Error())
	}
	if err := k.Keeper.RevokeDsc(ctx, cert.PublicKey.CanonicalBytes()); err != nil {
		return nil, err
	}
	return &types.MsgRevokeDscResponse{}, nil
}

// RevokeCsca withdraws trust from a Country Signing CA, so nothing it signs
// verifies from here on. Governance-gated, and the counterweight to AddCsca:
// without it, admitting a state to the trust store is irreversible, and a state
// in the trust store can mint passport identities this chain counts as distinct
// humans.
//
// Takes a certificate and revokes the key inside it — see Keeper.RevokeCsca for
// why the key is the unit and not the certificate.
func (k msgServer) RevokeCsca(ctx context.Context, req *types.MsgRevokeCsca) (*types.MsgRevokeCscaResponse, error) {
	if err := k.checkAuthority(req.Authority); err != nil {
		return nil, err
	}
	cert, err := certs.ParseCert(req.CertificateDer)
	if err != nil {
		return nil, types.ErrInvalidCert.Wrap(err.Error())
	}
	if err := k.Keeper.RevokeCsca(ctx, cert.PublicKey.CanonicalBytes()); err != nil {
		return nil, err
	}
	return &types.MsgRevokeCscaResponse{}, nil
}
