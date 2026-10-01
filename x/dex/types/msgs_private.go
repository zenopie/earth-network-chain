package types

import (
	"cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
	"github.com/earth-network/earth/zk/privacy"
)

// Private msg type URLs: the kind every signal binds first.
const (
	TypeMsgNoteSwap             = "/earth.dex.v1.MsgNoteSwap"
	TypeMsgAddLiquidityShielded = "/earth.dex.v1.MsgAddLiquidityShielded"
)

var (
	_ shieldedtypes.PrivateMsg       = (*MsgNoteSwap)(nil)
	_ shieldedtypes.FeeFromOutputMsg = (*MsgNoteSwap)(nil)
	_ shieldedtypes.MultiTransferMsg = (*MsgAddLiquidityShielded)(nil)

	_ sdk.HasValidateBasic = (*MsgNoteSwap)(nil)
	_ sdk.HasValidateBasic = (*MsgAddLiquidityShielded)(nil)
	_ sdk.HasValidateBasic = (*MsgBuyAnml)(nil)
)

func pcField(b []byte) (fr.Element, error) {
	e, err := privacy.FieldFromBytes(b)
	if err != nil {
		return e, errorsmod.Wrapf(ErrInvalidPrivateMsg, "pc: %v", err)
	}
	return e, nil
}

func checkNote(pc, ct []byte) error {
	if _, err := pcField(pc); err != nil {
		return err
	}
	if len(ct) > shieldedtypes.MaxCiphertextBytes {
		return errorsmod.Wrapf(ErrInvalidPrivateMsg, "ciphertext exceeds %d bytes", shieldedtypes.MaxCiphertextBytes)
	}
	return nil
}

// releases checks t moves a positive amount out of the pool.
func releases(t *shieldedtypes.Transfer, what string) error {
	if t.ValueOut == 0 || t.DenomOut == "" {
		return errorsmod.Wrapf(ErrInvalidPrivateMsg, "%s must release a positive amount", what)
	}
	return nil
}

// ---- MsgNoteSwap ------------------------------------------------------------

func (m *MsgNoteSwap) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

// OutputFee implements x/shielded's FeeFromOutputMsg.
func (m *MsgNoteSwap) OutputFee() uint64 { return m.FeeFromOutput }

// Signal binds the asset out, the slippage bound, where the output goes and
// the fee from output. The asset in is the transfer's asset_pub.
func (m *MsgNoteSwap) Signal(chainID string, _ address.Codec) (fr.Element, error) {
	pc, err := pcField(m.Pc)
	if err != nil {
		return fr.Element{}, err
	}
	return privacy.SpendSignal(TypeMsgNoteSwap, chainID, m.Transfer.Ciphertexts3(), privacy.Bytes([]byte(m.DenomOut)),
		privacy.U64(m.MinAmountOut), pc, privacy.Bytes(m.Ciphertext), privacy.U64(m.FeeFromOutput)), nil
}

func (m *MsgNoteSwap) ValidateBasic() error {
	if err := shieldedtypes.ValidateTransfers(m); err != nil {
		return err
	}
	if err := releases(&m.Transfer, "transfer"); err != nil {
		return err
	}
	if err := sdk.ValidateDenom(m.DenomOut); err != nil {
		return errorsmod.Wrap(ErrInvalidDenom, err.Error())
	}
	if m.DenomOut == m.Transfer.DenomOut {
		return errorsmod.Wrap(ErrInvalidDenom, "denom in and denom out must differ")
	}
	if m.MinAmountOut == 0 {
		// A note holds a positive value; a swap with no bound is a gift to
		// whoever orders the block.
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "min_amount_out must be positive")
	}
	if m.FeeFromOutput > 0 {
		if m.DenomOut != shieldedtypes.FeeDenom {
			return errorsmod.Wrapf(ErrInvalidPrivateMsg, "fee_from_output needs a swap into %s", shieldedtypes.FeeDenom)
		}
		if m.MinAmountOut <= m.FeeFromOutput {
			return errorsmod.Wrap(ErrInvalidPrivateMsg, "min_amount_out must exceed fee_from_output")
		}
	}
	return checkNote(m.Pc, m.Ciphertext)
}

// ---- MsgAddLiquidityShielded ------------------------------------------------

func (m *MsgAddLiquidityShielded) PrivateTransfer() *shieldedtypes.Transfer { return &m.Transfer }

// PrivateTransfers is the token transfer, then the ERTH transfer.
func (m *MsgAddLiquidityShielded) PrivateTransfers() []*shieldedtypes.Transfer {
	return []*shieldedtypes.Transfer{&m.Transfer, &m.ErthTransfer}
}

// ProviderBytes is the share recipient's raw address.
func (m *MsgAddLiquidityShielded) ProviderBytes(ac address.Codec) ([]byte, error) {
	bz, err := ac.StringToBytes(m.Provider)
	if err != nil {
		return nil, errorsmod.Wrapf(ErrInvalidPrivateMsg, "provider: %v", err)
	}
	return bz, nil
}

// Signal binds both transfers, the pool, the provider, the slippage bound and
// where refunds go.
func (m *MsgAddLiquidityShielded) Signal(chainID string, ac address.Codec) (fr.Element, error) {
	pc, err := pcField(m.RefundPc)
	if err != nil {
		return fr.Element{}, err
	}
	prov, err := m.ProviderBytes(ac)
	if err != nil {
		return fr.Element{}, err
	}
	return shieldedtypes.MultiSignal(TypeMsgAddLiquidityShielded, chainID, m.PrivateTransfers(), privacy.U64(m.PoolId),
		privacy.Bytes(prov), privacy.Bytes([]byte(m.MinShares)), pc, privacy.Bytes(m.RefundCiphertext))
}

func (m *MsgAddLiquidityShielded) ValidateBasic() error {
	if err := shieldedtypes.ValidateTransfers(m); err != nil {
		return err
	}
	if err := releases(&m.Transfer, "transfer"); err != nil {
		return err
	}
	if err := releases(&m.ErthTransfer, "erth_transfer"); err != nil {
		return err
	}
	if m.ErthTransfer.DenomOut != shieldedtypes.FeeDenom {
		return errorsmod.Wrapf(ErrInvalidDenom, "erth_transfer releases %s", shieldedtypes.FeeDenom)
	}
	if m.Provider == "" {
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "provider is required")
	}
	if m.MinShares != "" {
		if v, ok := math.NewIntFromString(m.MinShares); !ok || v.IsNegative() || v.String() != m.MinShares {
			return errorsmod.Wrap(ErrInvalidAmount, "min_shares must be a canonical non-negative integer")
		}
	}
	return checkNote(m.RefundPc, m.RefundCiphertext)
}

// ---- MsgBuyAnml -------------------------------------------------------------

func (m *MsgBuyAnml) ValidateBasic() error {
	if !m.TokenIn.IsValid() || !m.TokenIn.IsPositive() {
		return errorsmod.Wrap(ErrInvalidAmount, "token_in must be a positive coin")
	}
	if m.TokenIn.Denom == shieldedtypes.AnmlDenom {
		return errorsmod.Wrap(ErrInvalidDenom, "token_in cannot be ANML")
	}
	if m.MinAmountOut != "" {
		if v, ok := math.NewIntFromString(m.MinAmountOut); !ok || v.IsNegative() {
			return errorsmod.Wrap(ErrInvalidAmount, "invalid min_amount_out")
		}
	}
	return checkNote(m.Pc, m.Ciphertext)
}
