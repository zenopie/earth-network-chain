package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/zk/privacy"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// dscFacts is what the chain learns about the Document Signer behind a
// registration, recorded so a compromised signer's registrations stay findable.
type dscFacts struct {
	key     []byte // Poseidon2 commitment, as the proof exposed it
	country string // ISO 3166-1 alpha-2 of the issuing CSCA, "" if it names none
}

// preparedRegistration is a MsgRegister that passed every check but its
// proof: what the proof is verified against and what Register applies.
type preparedRegistration struct {
	vk        []byte
	pubInputs [][]byte
	nullifier []byte
	dsc       dscFacts
	// switched is decided here, before anything is written: a live
	// registration under the same passport makes this a switch, which is
	// neither rate-limited nor paid.
	switched bool
	// affiliate is the referrer's address, checked live; nil for none or on
	// a switch.
	affiliate sdk.AccAddress
}

// checkRegistration runs every check on a MsgRegister short of verifying its
// passport proof, cheapest first, and writes nothing. Everything checked is a
// public input the proof is then verified against, so checking before
// verifying is sound; it only moves who pays for a rejection.
func (k Keeper) checkRegistration(ctx context.Context, msg *types.MsgRegister) (preparedRegistration, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params, err := k.Params.Get(ctx)
	if err != nil {
		return preparedRegistration{}, err
	}
	vk, ok := params.VerifyingKeys[msg.SignatureAlgorithm]
	if !ok || len(vk) == 0 {
		return preparedRegistration{}, types.ErrNoVerifyingKey.Wrapf("algorithm %q", msg.SignatureAlgorithm)
	}
	n := len(msg.PublicSignals)
	for name, idx := range map[string]uint32{
		"nullifier": params.NullifierIndex, "address": params.AddressIndex, "current_date": params.CurrentDateIndex,
	} {
		if int(idx) >= n {
			return preparedRegistration{}, types.ErrBadPublicInputs.Wrapf("%s index %d out of range", name, idx)
		}
	}

	// Public inputs arrive as decimal field elements; UltraHonk wants 32-byte
	// big-endian ones. Non-canonical values are refused (see ParseSignal).
	pubInputs := make([][]byte, n)
	for i, s := range msg.PublicSignals {
		e, err := types.ParseSignal(s)
		if err != nil {
			return preparedRegistration{}, err
		}
		pubInputs[i] = privacy.FieldBytes(e)
	}

	// The binding. The proof travels in the clear, so anyone who reads a block
	// has its bytes; what stops them registering someone else's passport to an
	// identity and notes of their own is that the proof only verifies against
	// the address input it was made with, and the chain computes that input
	// from this msg's idc and pcs.
	binding, err := msg.Binding(k.addressCodec)
	if err != nil {
		return preparedRegistration{}, err
	}
	if !bytes.Equal(pubInputs[params.AddressIndex], privacy.FieldBytes(binding)) {
		return preparedRegistration{}, types.ErrBadPublicInputs.Wrap(
			"proof is bound to a different identity and notes than this msg names")
	}

	// Pin the prover-supplied current_date to the block time: the circuit
	// proves expiry >= current_date, which means nothing if current_date may be
	// backdated. Unconditional; a zero skew disables registration.
	if params.CurrentDateMaxSkewSeconds == 0 {
		return preparedRegistration{}, types.ErrBadPublicInputs.Wrap(
			"current_date_max_skew_seconds is unset; registration is disabled until governance sets it")
	}
	proofUnix, err := yymmddToUnix(pubInputs[params.CurrentDateIndex])
	if err != nil {
		return preparedRegistration{}, types.ErrBadPublicInputs.Wrap(err.Error())
	}
	skew := sdkCtx.BlockTime().Unix() - proofUnix
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(params.CurrentDateMaxSkewSeconds) {
		return preparedRegistration{}, types.ErrBadPublicInputs.Wrapf(
			"current_date out of range: proof skew %ds exceeds max %ds", skew, params.CurrentDateMaxSkewSeconds)
	}

	// Bind the proof to the Document Signer that signed the passport: the
	// certificate must chain to a trusted, unrevoked CSCA and its commitment
	// must equal the proof's dsc_key input.
	var facts dscFacts
	if k.pkiKeeper != nil {
		dscIndex := int(params.DscKeyIndex)
		if dscIndex >= n {
			return preparedRegistration{}, types.ErrBadPublicInputs.Wrapf("dsc key index %d out of range", dscIndex)
		}
		if len(msg.DscDer) == 0 {
			return preparedRegistration{}, types.ErrBadPublicInputs.Wrap("dsc certificate is required")
		}
		pubkey, country, err := k.pkiKeeper.VerifyDscIssuer(ctx, msg.DscDer)
		if err != nil {
			return preparedRegistration{}, err
		}
		commitment, err := certs.DscCommitmentOf(pubkey)
		if err != nil {
			return preparedRegistration{}, types.ErrBadPublicInputs.Wrap(err.Error())
		}
		want := commitment.Bytes()
		if !bytes.Equal(pubInputs[dscIndex], want[:]) {
			return preparedRegistration{}, types.ErrBadPublicInputs.Wrap("proof is not bound to the supplied DSC")
		}
		facts.key = append([]byte(nil), want[:]...)
		facts.country = country
	}

	nullifier := pubInputs[params.NullifierIndex]
	switched, err := k.isLiveRegistration(ctx, nullifier)
	if err != nil {
		return preparedRegistration{}, err
	}
	// A switch to the identity already registered changes nothing the holder
	// wants, and is what a replay of the registration that made it looks like:
	// the proof is public, and replaying it within the current_date skew would
	// otherwise zero the holder's leaf and restart its activation delay.
	if switched {
		live, err := k.Registrations.Get(ctx, nullifier)
		if err != nil {
			return preparedRegistration{}, err
		}
		if bytes.Equal(live.Idc, msg.Idc) {
			return preparedRegistration{}, types.ErrRegistrationReplay
		}
	}
	// A paid registration's affiliate must be a live referrer. A switch pays
	// nothing, so its affiliate is not looked at.
	var affiliate []byte
	if !switched && msg.Affiliate != "" {
		if affiliate, err = k.addressCodec.StringToBytes(msg.Affiliate); err != nil {
			return preparedRegistration{}, errorsmod.Wrapf(types.ErrInvalidMsg, "affiliate: %v", err)
		}
		live, _, err := k.liveReferrer(ctx, affiliate)
		if err != nil {
			return preparedRegistration{}, err
		}
		if !live {
			return preparedRegistration{}, errorsmod.Wrapf(types.ErrNoReferrer, "affiliate %s", msg.Affiliate)
		}
	}
	// Rate caps, before the proof: a country at its cap should not cost a
	// verification per refused attempt. Only once the certificate has chained
	// to a trusted CSCA above, so a claimed signer cannot be pushed to its
	// limit with junk. A switch moves a person already counted and is exempt.
	if !switched {
		if err := k.checkRegistrationRate(ctx, facts.key, facts.country); err != nil {
			return preparedRegistration{}, err
		}
	}
	return preparedRegistration{vk: vk, pubInputs: pubInputs, nullifier: nullifier, dsc: facts, switched: switched, affiliate: affiliate}, nil
}

// verifyRegistrationProof verifies the passport proof checkRegistration
// prepared.
func verifyRegistrationProof(msg *types.MsgRegister, p preparedRegistration) error {
	valid, err := ultrahonk.Verify(p.vk, msg.Proof, p.pubInputs)
	if err != nil {
		return types.ErrInvalidProof.Wrap(err.Error())
	}
	if !valid {
		return types.ErrInvalidProof
	}
	return nil
}

// yymmddToUnix parses a public-input field element holding a YYMMDD date (how the
// circuit encodes current_date) into a UTC unix timestamp at midnight. The two-
// digit year is interpreted as 2000-2099 (passports do not predate 2000).
func yymmddToUnix(b []byte) (int64, error) {
	n := new(big.Int).SetBytes(b).Int64()
	if n < 0 || n > 999999 {
		return 0, errors.New("current_date is not a YYMMDD value")
	}
	yy := int(n / 10000)
	mm := int((n / 100) % 100)
	dd := int(n % 100)
	if mm < 1 || mm > 12 || dd < 1 || dd > 31 {
		return 0, errors.New("current_date has an invalid month or day")
	}
	return time.Date(2000+yy, time.Month(mm), dd, 0, 0, 0, 0, time.UTC).Unix(), nil
}

// hexOf renders an id for an event attribute.
func hexOf(b []byte) string { return hex.EncodeToString(b) }

// isLiveRegistration reports whether a passport nullifier already has an
// unexpired registration, which makes registering it again a switch.
func (k Keeper) isLiveRegistration(ctx context.Context, nullifier []byte) (bool, error) {
	reg, err := k.Registrations.Get(ctx, nullifier)
	if errors.Is(err, collections.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expired, err := k.isExpired(ctx, reg)
	return !expired, err
}

func (k Keeper) isExpired(ctx context.Context, reg types.Registration) (bool, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return false, err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	return now > reg.RegisteredAt+int64(params.RegistrationValiditySeconds), nil
}

// getRegCount returns the number of registrations (default 0).
func (k Keeper) getRegCount(ctx context.Context) (uint64, error) {
	v, err := k.RegCount.Get(ctx)
	if errors.Is(err, collections.ErrNotFound) {
		return 0, nil
	}
	return v, err
}

// addRegistration writes a registration and every index over it.
func (k Keeper) addRegistration(ctx context.Context, reg types.Registration) error {
	if err := k.Registrations.Set(ctx, reg.Nullifier, reg); err != nil {
		return err
	}
	if err := k.RegByRegisteredAt.Set(ctx, collections.Join(reg.RegisteredAt, reg.Nullifier)); err != nil {
		return err
	}
	if len(reg.DscKey) > 0 {
		if err := k.RegByDsc.Set(ctx, collections.Join(reg.DscKey, reg.Nullifier)); err != nil {
			return err
		}
	}
	if err := bumpCount(ctx, k.RegCountByDsc, reg.DscKey); err != nil {
		return err
	}
	if err := bumpCount(ctx, k.RegCountByCountry, reg.Country); err != nil {
		return err
	}
	count, err := k.getRegCount(ctx)
	if err != nil {
		return err
	}
	return k.RegCount.Set(ctx, count+1)
}

// removeRegistration retires a registration: its leaf is zeroed, so its
// holder stops proving membership once the roots from before this block age
// out, and its record, indexes and tallies go.
//
// The chain cannot find what the holder did with their membership: their
// claims and votes are filed under nullifiers only they can link to this
// leaf. Those are bounded instead: a caretaker split lapses within
// caretaker_vote_seconds, a claim is a day's, and a vote is its ballot's.
func (k Keeper) removeRegistration(ctx context.Context, reg types.Registration) error {
	if err := k.zeroLeaf(ctx, reg.LeafIndex); err != nil {
		return err
	}
	if err := k.Registrations.Remove(ctx, reg.Nullifier); err != nil {
		return err
	}
	if err := k.RegByRegisteredAt.Remove(ctx, collections.Join(reg.RegisteredAt, reg.Nullifier)); err != nil {
		return err
	}
	if len(reg.DscKey) > 0 {
		if err := k.RegByDsc.Remove(ctx, collections.Join(reg.DscKey, reg.Nullifier)); err != nil {
			return err
		}
	}
	if err := dropCount(ctx, k.RegCountByDsc, reg.DscKey); err != nil {
		return err
	}
	if err := dropCount(ctx, k.RegCountByCountry, reg.Country); err != nil {
		return err
	}
	count, err := k.getRegCount(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return k.RegCount.Set(ctx, count-1)
	}
	return nil
}

// sweepExpiredRegistrations retires registrations whose validity window has
// closed, zeroing their leaves. budget is the share of this block's shared
// retirement budget left after the revoked-signer purge; it returns how many
// it used.
func (k Keeper) sweepExpiredRegistrations(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	params, err := k.Params.Get(ctx)
	if err != nil {
		return 0, err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	cutoff := now - int64(params.RegistrationValiditySeconds)
	if cutoff <= 0 {
		return 0, nil
	}

	// Collect first: removeRegistration writes to the index being walked.
	var expired []collections.Pair[int64, []byte]
	err = k.RegByRegisteredAt.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		// isExpired is now > registered_at + validity, i.e. registered_at < cutoff.
		if key.K1() >= cutoff {
			return true, nil
		}
		expired = append(expired, key)
		return len(expired) >= budget, nil
	})
	if err != nil {
		return 0, err
	}
	for _, key := range expired {
		reg, err := k.Registrations.Get(ctx, key.K2())
		if errors.Is(err, collections.ErrNotFound) {
			if err := k.RegByRegisteredAt.Remove(ctx, key); err != nil {
				return 0, err
			}
			continue
		} else if err != nil {
			return 0, err
		}
		if err := k.removeRegistration(ctx, reg); err != nil {
			return 0, err
		}
	}
	if len(expired) >= budget {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
			"registration_sweep_capped",
			sdk.NewAttribute("retired", strconv.Itoa(len(expired))),
			sdk.NewAttribute("limit", strconv.Itoa(budget)),
			sdk.NewAttribute("reason", "expired"),
		))
	}
	return len(expired), nil
}
