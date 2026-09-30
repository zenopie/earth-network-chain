package types

import (
	"cosmossdk.io/x/tx/signing"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func RegisterInterfaces(registrar codectypes.InterfaceRegistry) {
	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgVoteProposal{},
		&MsgProposeRemoval{},
		&MsgVoteRemoval{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}

func noSigners(name protoreflect.FullName) signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: name,
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}

// Every msg of the chamber is private and has no signers; see x/shielded
// ProvideTransferGetSigners. One provider per msg type.

func ProvideVoteProposalGetSigners() signing.CustomGetSigner {
	return noSigners("earth.assembly.v1.MsgVoteProposal")
}

func ProvideProposeRemovalGetSigners() signing.CustomGetSigner {
	return noSigners("earth.assembly.v1.MsgProposeRemoval")
}

func ProvideVoteRemovalGetSigners() signing.CustomGetSigner {
	return noSigners("earth.assembly.v1.MsgVoteRemoval")
}
