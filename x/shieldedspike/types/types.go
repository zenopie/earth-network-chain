// Package types is the Phase 0 spike for unsigned, pool-paid private txs.
// Throwaway: it exists to prove the SDK v0.53 mechanism, not to ship.
package types

import (
	"crypto/sha256"
	"fmt"

	"cosmossdk.io/math"
	"cosmossdk.io/x/tx/signing"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	ModuleName = "shieldedspike"
	StoreKey   = ModuleName

	// FeeDenom is the only denom a private fee may be paid in.
	FeeDenom = "uerth"

	// ProofVerifyGas is the fixed gas charged per private msg for proof
	// verification, whether or not the proof verifies, so simulate is exact.
	ProofVerifyGas uint64 = 150_000

	// DefaultMinFee is the consensus floor on a private tx's total fee. It is
	// clamped to >= 1 at read time, so there is no configuration of the chain
	// that admits a zero-fee private tx.
	DefaultMinFee int64 = 1000
)

var (
	NullifierPrefix = []byte{0x01}
	MinFeeKey       = []byte{0x02}
)

// PrivateMsg is what the ante router keys on: a msg with no signers whose
// authorization is a proof and whose replay protection is a nullifier.
type PrivateMsg interface {
	sdk.Msg
	PrivateFee() (math.Int, error)
	PrivateNullifier() []byte
	PrivateProof() []byte
}

var _ PrivateMsg = (*MsgPrivateNoop)(nil)

func (m *MsgPrivateNoop) PrivateFee() (math.Int, error) {
	fee, ok := math.NewIntFromString(m.Fee)
	if !ok || fee.IsNegative() {
		return math.Int{}, fmt.Errorf("invalid fee %q", m.Fee)
	}
	return fee, nil
}
func (m *MsgPrivateNoop) PrivateNullifier() []byte { return m.Nullifier }
func (m *MsgPrivateNoop) PrivateProof() []byte     { return m.Proof }

// ValidateBasic runs in baseapp before the ante (validateBasicTxMsgs).
func (m *MsgPrivateNoop) ValidateBasic() error {
	if len(m.Nullifier) != 32 {
		return fmt.Errorf("nullifier must be 32 bytes")
	}
	if _, err := m.PrivateFee(); err != nil {
		return err
	}
	if len(m.Proof) == 0 {
		return fmt.Errorf("empty proof")
	}
	return nil
}

// StubProof is the spike's stand-in for a transfer proof: a hash over the
// public inputs it binds (nullifier, fee). A real proof binds the same inputs.
func StubProof(nullifier []byte, fee string) []byte {
	h := sha256.New()
	h.Write([]byte("shieldedspike/v1"))
	h.Write(nullifier)
	h.Write([]byte(fee))
	return h.Sum(nil)
}

// ProvideCustomGetSigners declares MsgPrivateNoop's signers to be empty. It is
// the only way such a msg can exist at all: runtime.ProvideInterfaceRegistry
// runs SigningContext().Validate(), which fails app construction for any Msg
// without a cosmos.msg.v1.signer option and without a custom getter.
func ProvideCustomGetSigners() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("earth.shieldedspike.v1.MsgPrivateNoop"),
		Fn:      func(proto.Message) ([][]byte, error) { return [][]byte{}, nil },
	}
}

func RegisterInterfaces(registrar codectypes.InterfaceRegistry) {
	registrar.RegisterImplementations((*sdk.Msg)(nil), &MsgPrivateNoop{})
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}
