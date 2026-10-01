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
		&MsgSwap{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgRemoveLiquidity{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgAddLiquidity{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgCreatePool{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgUpdateParams{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgNoteSwap{},
		&MsgBuyAnml{},
		&MsgAddLiquidityShielded{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}

// The private msgs have no signers (see x/shielded/types.ProvideTransferGetSigners).
// depinject collects signing.CustomGetSigner one provider at a time, hence one
// function per msg.
func noSigners(name string) signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("earth.dex.v1." + name),
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}

func ProvideNoteSwapGetSigners() signing.CustomGetSigner { return noSigners("MsgNoteSwap") }
func ProvideAddLiquidityShieldedGetSigners() signing.CustomGetSigner {
	return noSigners("MsgAddLiquidityShielded")
}
