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

// Private msg type URLs: the kind every sighash binds first.
const (
	TypeMsgNoteSwap                = "/earth.dex.v1.MsgNoteSwap"
	TypeMsgAddLiquidityShielded    = "/earth.dex.v1.MsgAddLiquidityShielded"
	TypeMsgRemoveLiquidityShielded = "/earth.dex.v1.MsgRemoveLiquidityShielded"
)

var (
	_ shieldedtypes.PrivateMsg       = (*MsgNoteSwap)(nil)
	_ shieldedtypes.FeeFromOutputMsg = (*MsgNoteSwap)(nil)
	_ shieldedtypes.PrivateMsg       = (*MsgAddLiquidityShielded)(nil)
	_ shieldedtypes.PrivateMsg       = (*MsgRemoveLiquidityShielded)(nil)

	_ sdk.HasValidateBasic = (*MsgNoteSwap)(nil)
	_ sdk.HasValidateBasic = (*MsgAddLiquidityShielded)(nil)
	_ sdk.HasValidateBasic = (*MsgRemoveLiquidityShielded)(nil)
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

// remainders is msg's release map after its fee, refusing an empty one.
func remainders(msg shieldedtypes.PrivateMsg) ([]shieldedtypes.Remainder, error) {
	if err := shieldedtypes.ValidateBundles(msg); err != nil {
		return nil, err
	}
	rem, err := shieldedtypes.Remainders(msg)
	if err != nil {
		return nil, err
	}
	if len(rem) == 0 {
		return nil, errorsmod.Wrap(ErrInvalidPrivateMsg, "the bundle must release a positive amount beyond its fee")
	}
	return rem, nil
}

// ---- MsgNoteSwap ------------------------------------------------------------

func (m *MsgNoteSwap) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Bundle}
}

func (m *MsgNoteSwap) PrivateFee() uint64 { return m.Fee }

// OutputFee implements x/shielded's FeeFromOutputMsg.
func (m *MsgNoteSwap) OutputFee() uint64 { return m.FeeFromOutput }

// In is the asset swapped in: the release map's one remainder (zero coin if
// the msg is malformed; ValidateBasic refuses it).
func (m *MsgNoteSwap) In() sdk.Coin {
	rem, err := shieldedtypes.Remainders(m)
	if err != nil || len(rem) != 1 {
		return sdk.Coin{}
	}
	return sdk.NewCoin(rem[0].Denom, math.NewIntFromUint64(rem[0].Amount))
}

// SighashFields binds the asset out, the slippage bound, where the output
// goes and both fees. The asset in is the bundle's balance.
func (m *MsgNoteSwap) SighashFields(address.Codec) ([]fr.Element, error) {
	pc, err := pcField(m.Pc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.Bytes([]byte(m.DenomOut)), privacy.U64(m.MinAmountOut), pc,
		privacy.Bytes(m.Ciphertext), privacy.U64(m.FeeFromOutput), privacy.U64(m.Fee)}, nil
}

func (m *MsgNoteSwap) ValidateBasic() error {
	rem, err := remainders(m)
	if err != nil {
		return err
	}
	if len(rem) != 1 {
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "a swap releases one asset beyond its fee")
	}
	if (m.Fee == 0) == (m.FeeFromOutput == 0) {
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "the fee is paid by the bundle or from the output, exactly one")
	}
	if err := sdk.ValidateDenom(m.DenomOut); err != nil {
		return errorsmod.Wrap(ErrInvalidDenom, err.Error())
	}
	if m.DenomOut == rem[0].Denom {
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

func (m *MsgAddLiquidityShielded) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Bundle}
}

func (m *MsgAddLiquidityShielded) PrivateFee() uint64 { return m.Fee }

// Legs is the deposit: the ERTH leg (the uerth balance less the fee) and the
// token leg (the other balance). Zero coins if the msg is malformed.
func (m *MsgAddLiquidityShielded) Legs() (erth, token sdk.Coin) {
	rem, err := shieldedtypes.Remainders(m)
	if err != nil || len(rem) != 2 {
		return sdk.Coin{}, sdk.Coin{}
	}
	for _, r := range rem {
		c := sdk.NewCoin(r.Denom, math.NewIntFromUint64(r.Amount))
		if r.Denom == shieldedtypes.FeeDenom {
			erth = c
		} else {
			token = c
		}
	}
	return erth, token
}

// SighashFields binds the pool, the slippage bound, where the shares and the
// refunds go and the fee.
func (m *MsgAddLiquidityShielded) SighashFields(address.Codec) ([]fr.Element, error) {
	sharePc, err := pcField(m.SharePc)
	if err != nil {
		return nil, err
	}
	refundPc, err := pcField(m.RefundPc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.U64(m.PoolId), privacy.Bytes([]byte(m.MinShares)), sharePc,
		privacy.Bytes(m.ShareCiphertext), refundPc, privacy.Bytes(m.RefundCiphertext), privacy.U64(m.Fee)}, nil
}

func (m *MsgAddLiquidityShielded) ValidateBasic() error {
	rem, err := remainders(m)
	if err != nil {
		return err
	}
	if m.Fee == 0 {
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "the bundle pays a positive fee")
	}
	if erth, token := m.Legs(); len(rem) != 2 || !erth.IsValid() || !token.IsValid() || erth.IsZero() || token.IsZero() {
		return errorsmod.Wrapf(ErrInvalidDenom, "the bundle releases %s and the pool's token beyond its fee", shieldedtypes.FeeDenom)
	}
	if m.MinShares != "" {
		if v, ok := math.NewIntFromString(m.MinShares); !ok || v.IsNegative() || v.String() != m.MinShares {
			return errorsmod.Wrap(ErrInvalidAmount, "min_shares must be a canonical non-negative integer")
		}
	}
	if err := checkNote(m.SharePc, m.ShareCiphertext); err != nil {
		return err
	}
	return checkNote(m.RefundPc, m.RefundCiphertext)
}

// ---- MsgRemoveLiquidityShielded ------------------------------------------------

func (m *MsgRemoveLiquidityShielded) PrivateBundles() []*shieldedtypes.Bundle {
	return []*shieldedtypes.Bundle{&m.Bundle}
}

func (m *MsgRemoveLiquidityShielded) PrivateFee() uint64 { return m.Fee }

// Shares is the LP shares withdrawn: the bundle's dexlp/<pool_id> balance.
func (m *MsgRemoveLiquidityShielded) Shares() sdk.Coin {
	denom := LPShareDenom(m.PoolId)
	return sdk.NewCoin(denom, math.NewIntFromUint64(m.Bundle.Balance(denom)))
}

// WithdrawalID keys the withdrawal in place of an account: 0x00 || the first
// nullifier the bundle spends, unique for ever.
func (m *MsgRemoveLiquidityShielded) WithdrawalID() []byte {
	if len(m.Bundle.Actions) == 0 {
		return nil
	}
	return append([]byte{0}, m.Bundle.Actions[0].Nullifier...)
}

// SighashFields binds the pool, where both legs go and the fee.
func (m *MsgRemoveLiquidityShielded) SighashFields(address.Codec) ([]fr.Element, error) {
	erthPc, err := pcField(m.ErthPc)
	if err != nil {
		return nil, err
	}
	tokenPc, err := pcField(m.TokenPc)
	if err != nil {
		return nil, err
	}
	return []fr.Element{privacy.U64(m.PoolId), erthPc, privacy.Bytes(m.ErthCiphertext), tokenPc,
		privacy.Bytes(m.TokenCiphertext), privacy.U64(m.Fee)}, nil
}

func (m *MsgRemoveLiquidityShielded) ValidateBasic() error {
	rem, err := remainders(m)
	if err != nil {
		return err
	}
	if m.Fee == 0 {
		return errorsmod.Wrap(ErrInvalidPrivateMsg, "the bundle pays a positive fee")
	}
	if len(rem) != 1 || rem[0].Denom != LPShareDenom(m.PoolId) {
		return errorsmod.Wrapf(ErrInvalidDenom, "the bundle releases %s and nothing else beyond its fee", LPShareDenom(m.PoolId))
	}
	if err := checkNote(m.ErthPc, m.ErthCiphertext); err != nil {
		return err
	}
	return checkNote(m.TokenPc, m.TokenCiphertext)
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
