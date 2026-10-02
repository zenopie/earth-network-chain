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
		&MsgDelegate{},
		&MsgRestake{},
		&MsgUndelegate{},
		&MsgClaimUnbonding{},
		&MsgStakeVote{},
		&MsgLockPosition{},
		&MsgUpdatePosition{},
		&MsgUnlockPosition{},
		&MsgPositionVote{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}

// Every private msg has no signers (see x/shielded/types.ProvideSendGetSigners).
// depinject collects signing.CustomGetSigner one provider at a time, hence one
// function per msg.
func noSigners(name string) signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("earth.shieldedstaking.v1." + name),
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}

func ProvideDelegateGetSigners() signing.CustomGetSigner       { return noSigners("MsgDelegate") }
func ProvideRestakeGetSigners() signing.CustomGetSigner        { return noSigners("MsgRestake") }
func ProvideUndelegateGetSigners() signing.CustomGetSigner     { return noSigners("MsgUndelegate") }
func ProvideClaimGetSigners() signing.CustomGetSigner          { return noSigners("MsgClaimUnbonding") }
func ProvideStakeVoteGetSigners() signing.CustomGetSigner      { return noSigners("MsgStakeVote") }
func ProvideLockPositionGetSigners() signing.CustomGetSigner   { return noSigners("MsgLockPosition") }
func ProvideUpdatePositionGetSigners() signing.CustomGetSigner { return noSigners("MsgUpdatePosition") }
func ProvideUnlockPositionGetSigners() signing.CustomGetSigner { return noSigners("MsgUnlockPosition") }
func ProvidePositionVoteGetSigners() signing.CustomGetSigner   { return noSigners("MsgPositionVote") }
