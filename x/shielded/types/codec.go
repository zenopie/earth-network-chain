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
		&MsgUpdateParams{},
		&MsgRegisterAsset{},
		&MsgShield{},
		&MsgSend{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}

// ProvideSendGetSigners declares MsgSend's signers to be empty. It is
// the only way such a msg can exist at all: runtime.ProvideInterfaceRegistry
// validates the signing context and fails app construction for any Msg
// without a cosmos.msg.v1.signer option and without a custom getter.
//
// Provided on its own, with no inputs, because the interface registry is
// built before any keeper: taking it from ProvideModule would be a cycle.
func ProvideSendGetSigners() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("earth.shielded.v1.MsgSend"),
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}
