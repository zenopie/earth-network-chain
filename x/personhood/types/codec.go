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
		&MsgRegister{},
		&MsgClaimAnml{},
		&MsgSetCaretaker{},
		&MsgBindHandle{},
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}

func noSigners(name protoreflect.FullName) signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: name,
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}

// The private msgs have no signers; see x/shielded ProvideSendGetSigners
// for why each needs a custom getter to exist at all. One provider per msg:
// depinject collects CustomGetSigner values one per provider.

func ProvideRegisterGetSigners() signing.CustomGetSigner {
	return noSigners("earth.personhood.v1.MsgRegister")
}

func ProvideClaimAnmlGetSigners() signing.CustomGetSigner {
	return noSigners("earth.personhood.v1.MsgClaimAnml")
}

func ProvideSetCaretakerGetSigners() signing.CustomGetSigner {
	return noSigners("earth.personhood.v1.MsgSetCaretaker")
}

func ProvideBindHandleGetSigners() signing.CustomGetSigner {
	return noSigners("earth.personhood.v1.MsgBindHandle")
}
