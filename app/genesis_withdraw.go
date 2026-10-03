package app

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	sstypes "github.com/earth-network/earth/x/shieldedstaking/types"
)

// ValidateOperatorWithdrawAddrs refuses a genesis in which a validator's
// operator — a staking genesis validator or a gentx's MsgCreateValidator —
// has a withdraw address (x/distribution delegator_withdraw_infos) pointing
// anywhere but itself (the default, which InitGenesis turns into the
// escrow) or its reward escrow (sstypes.RewardEscrowAddress; an exported
// genesis carries it): its rewards would be liquid. The cross-module
// half of `earthd genesis validate`, which otherwise checks each module
// alone; x/shieldedstaking's InitGenesis refuses the same state on chain.
func ValidateOperatorWithdrawAddrs(cdc codec.JSONCodec, txDecoder sdk.TxDecoder, appState map[string]json.RawMessage) error {
	var distr distrtypes.GenesisState
	if bz := appState[distrtypes.ModuleName]; bz != nil {
		if err := cdc.UnmarshalJSON(bz, &distr); err != nil {
			return fmt.Errorf("distribution genesis: %w", err)
		}
	}
	if len(distr.DelegatorWithdrawInfos) == 0 {
		return nil
	}
	foreign := map[string]string{}
	modules := map[string]string{}
	for name := range GetMaccPerms() {
		modules[authtypes.NewModuleAddress(name).String()] = name
	}
	for _, wi := range distr.DelegatorWithdrawInfos {
		if wi.DelegatorAddress != wi.WithdrawAddress {
			foreign[wi.DelegatorAddress] = wi.WithdrawAddress
			// A module account's rewards (x/shieldedstaking's are every
			// private staker's) are paid to the module itself (audit 4, G2).
			if name, ok := modules[wi.DelegatorAddress]; ok {
				return fmt.Errorf("the %s module account has withdraw address %s: a module account's rewards stay with it", name, wi.WithdrawAddress)
			}
		}
	}
	check := func(valoper string) error {
		bz, err := sdk.ValAddressFromBech32(valoper)
		if err != nil {
			return fmt.Errorf("validator %s: %w", valoper, err)
		}
		op := sdk.AccAddress(bz).String()
		if wa, ok := foreign[op]; ok && wa != sstypes.RewardEscrowAddress(bz).String() {
			return fmt.Errorf("validator operator %s has withdraw address %s: an operator's rewards are paid to its reward escrow (its self-bond compounds)", op, wa)
		}
		return nil
	}

	var staking stakingtypes.GenesisState
	if bz := appState[stakingtypes.ModuleName]; bz != nil {
		if err := cdc.UnmarshalJSON(bz, &staking); err != nil {
			return fmt.Errorf("staking genesis: %w", err)
		}
	}
	for _, v := range staking.Validators {
		if err := check(v.OperatorAddress); err != nil {
			return err
		}
	}

	var genutil genutiltypes.GenesisState
	if bz := appState[genutiltypes.ModuleName]; bz != nil {
		if err := cdc.UnmarshalJSON(bz, &genutil); err != nil {
			return fmt.Errorf("genutil genesis: %w", err)
		}
	}
	for i, raw := range genutil.GenTxs {
		tx, err := txDecoder(raw)
		if err != nil {
			return fmt.Errorf("gentx %d: %w", i, err)
		}
		for _, m := range tx.GetMsgs() {
			if cv, ok := m.(*stakingtypes.MsgCreateValidator); ok {
				if err := check(cv.ValidatorAddress); err != nil {
					return fmt.Errorf("gentx %d: %w", i, err)
				}
			}
		}
	}
	return nil
}
