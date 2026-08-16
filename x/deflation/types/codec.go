package types

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterInterfaces(registrar codectypes.InterfaceRegistry) {
	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgAddIntegratedOption{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgResetAllocations{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgAddAddressOption{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgClaimAllocation{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSetAllocations{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}
