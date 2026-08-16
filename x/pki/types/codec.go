package types

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterInterfaces(registrar codectypes.InterfaceRegistry) {
	registrar.RegisterImplementations((*sdk.Msg)(nil), &MsgUpdateParams{})
	registrar.RegisterImplementations((*sdk.Msg)(nil), &MsgAddCsca{})
	registrar.RegisterImplementations((*sdk.Msg)(nil), &MsgRevokeDsc{})
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}
