package keeper

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strconv"
	"time"

	"cosmossdk.io/collections"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/x/pki/certs"
	"github.com/earth-network/earth/zk/ultrahonk"
)

// verifyRegistrationProof verifies a Barretenberg UltraHonk (bb v5.0.0, poseidon2)
// registration proof against the verifying key selected by algorithm, binds it to
// the Document Signer certificate dscDER, and returns the nullifier public input
// as a 32-byte big-endian field element. Public-input positions are
// governance-configured (params.NullifierIndex / params.DscKeyIndex /
// params.CurrentDateIndex) since they are circuit-version specific.
// dscFacts is what the chain learns about the Document Signer behind a
// registration, recorded so a compromised signer's registrations stay findable.
type dscFacts struct {
	key     []byte // Poseidon2 commitment, as the proof exposed it
	country string // ISO 3166-1 alpha-2, "" if the certificate omits it
}

func (k Keeper) verifyRegistrationProof(ctx context.Context, proof []byte, publicSignals []string, algorithm string, dscDER []byte) ([]byte, dscFacts, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, dscFacts{}, err
	}
	var facts dscFacts
	vk, ok := params.VerifyingKeys[algorithm]
	if !ok || len(vk) == 0 {
		return nil, dscFacts{}, types.ErrNoVerifyingKey.Wrapf("algorithm %q", algorithm)
	}
	nullifierIndex := int(params.NullifierIndex)
	if nullifierIndex >= len(publicSignals) {
		return nil, dscFacts{}, types.ErrBadPublicInputs.Wrapf("nullifier index %d out of range", nullifierIndex)
	}

	// Public inputs arrive as decimal field elements; UltraHonk wants 32-byte
	// big-endian field elements, passed separately from the proof.
	pubInputs := make([][]byte, len(publicSignals))
	for i, s := range publicSignals {
		b, err := signalToBytes32(s)
		if err != nil {
			return nil, dscFacts{}, types.ErrBadPublicInputs.Wrap(err.Error())
		}
		pubInputs[i] = b
	}

	valid, err := ultrahonk.Verify(vk, proof, pubInputs)
	if err != nil {
		return nil, dscFacts{}, types.ErrInvalidProof.Wrap(err.Error())
	}
	if !valid {
		return nil, dscFacts{}, types.ErrInvalidProof
	}

	// Bind the proof to the Document Signer that actually signed the passport.
	// The circuit proved the SOD was signed by the key committed to at
	// dsc_key_index; the chain independently verifies the accompanying
	// certificate chains to a trusted CSCA and is unrevoked, then requires the
	// commitment it derives to equal that public input. Without this equality a
	// prover could sign with any key while naming a trusted one.
	if k.pkiKeeper != nil {
		dscIndex := int(params.DscKeyIndex)
		if dscIndex >= len(pubInputs) {
			return nil, dscFacts{}, types.ErrBadPublicInputs.Wrapf("dsc key index %d out of range", dscIndex)
		}
		if len(dscDER) == 0 {
			return nil, dscFacts{}, types.ErrBadPublicInputs.Wrap("dsc certificate is required")
		}
		pubkey, err := k.pkiKeeper.VerifyDsc(ctx, dscDER)
		if err != nil {
			return nil, dscFacts{}, err
		}
		commitment := certs.DscCommitment(pubkey)
		want := commitment.Bytes()
		if !bytes.Equal(pubInputs[dscIndex], want[:]) {
			return nil, dscFacts{}, types.ErrBadPublicInputs.Wrap("proof is not bound to the supplied DSC")
		}
		facts.key = append([]byte(nil), want[:]...)
		if cert, err := certs.ParseCert(dscDER); err == nil {
			facts.country = cert.Country()
		}
	}

	// Pin the prover-supplied current_date to the block time. The circuit proves
	// the passport's expiry >= current_date, but current_date is a prover input,
	// not a chain fact: left unchecked, the holder of a passport that expired in
	// 2019 can claim current_date is 2015, and "2019 >= 2015" proves out fine.
	// Requiring it to sit within skew of the block time is what makes the
	// circuit's expiry constraint mean anything.
	//
	// Unconditional on purpose. Params.Validate rejects a zero skew, but genesis
	// state can reach the store without passing through it, and a zero here used
	// to skip the block entirely — the strictest-looking value silently being
	// the most permissive one. Failing closed costs a misconfigured chain its
	// registrations; failing open costs it the expiry invariant.
	if params.CurrentDateMaxSkewSeconds == 0 {
		return nil, dscFacts{}, types.ErrBadPublicInputs.Wrap(
			"current_date_max_skew_seconds is unset; registration is disabled until governance sets it")
	}
	dateIndex := int(params.CurrentDateIndex)
	if dateIndex >= len(pubInputs) {
		return nil, dscFacts{}, types.ErrBadPublicInputs.Wrapf("current_date index %d out of range", dateIndex)
	}
	proofUnix, err := yymmddToUnix(pubInputs[dateIndex])
	if err != nil {
		return nil, dscFacts{}, types.ErrBadPublicInputs.Wrap(err.Error())
	}
	blockUnix := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	skew := blockUnix - proofUnix
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(params.CurrentDateMaxSkewSeconds) {
		return nil, dscFacts{}, types.ErrBadPublicInputs.Wrapf(
			"current_date out of range: proof skew %ds exceeds max %ds",
			skew, params.CurrentDateMaxSkewSeconds)
	}

	return pubInputs[nullifierIndex], facts, nil
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

// signalToBytes32 parses a decimal public signal into a 32-byte big-endian
// representation (BN254 field elements fit in 32 bytes).
//
// Signals must be canonical — strictly less than the BN254 scalar field prime.
// The prime is just under 2^254, so a merely-fits-in-32-bytes check would also
// admit n+p, n+2p, … which name the same field element as n. The verifier
// reduces mod p, so those all verify against a proof built for n, while the
// nullifier they produce differs byte-for-byte. Since the nullifier is the
// dedup key for "this human already registered", accepting them would let one
// passport register arbitrarily many times.
func signalToBytes32(s string) ([]byte, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.Cmp(fr.Modulus()) >= 0 {
		return nil, types.ErrBadPublicInputs.Wrapf("bad public signal %q", s)
	}
	var buf [32]byte
	n.FillBytes(buf[:])
	return buf[:], nil
}

// getRegistrationByAddr returns the registration for a wallet, if any.
func (k Keeper) getRegistrationByAddr(ctx context.Context, addr sdk.AccAddress) (types.Registration, bool, error) {
	nullifier, err := k.RegByAddr.Get(ctx, addr.Bytes())
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.Registration{}, false, nil
		}
		return types.Registration{}, false, err
	}
	reg, err := k.Registrations.Get(ctx, nullifier)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.Registration{}, false, nil
		}
		return types.Registration{}, false, err
	}
	return reg, true, nil
}

// isExpired reports whether a registration is past the validity window.
func (k Keeper) isExpired(ctx context.Context, reg types.Registration) (bool, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return false, err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	return now > reg.RegisteredAt+int64(params.RegistrationValiditySeconds), nil
}

// getRegCount returns the number of registered humans (default 0).
func (k Keeper) getRegCount(ctx context.Context) (uint64, error) {
	v, err := k.RegCount.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// removeRegistration retires a registration completely: its indexes, the
// headcount and per-signer/per-country tallies, and — importantly — the
// allocation vote weight it was carrying.
//
// The vote weight is the part that is easy to miss. A registration that lapses
// without being cleaned up leaves its percentages sitting in the human stream's
// total weight forever, diluting every voter who is still a verified human,
// while the options it pointed at keep accruing ERTH that no live human directs.
//
// Callers must have advanced the human stream to the current block first —
// ClearVoter credits against its index. The sweep retires up to a full batch per
// block, so advancing once per block there beats re-advancing per registration.
func (k Keeper) removeRegistration(ctx context.Context, reg types.Registration) error {
	addrBz, err := k.addressCodec.StringToBytes(reg.Address)
	if err != nil {
		return err
	}

	if err := k.allocationKeeper.ClearVoter(ctx, types.AllocationStream, addrBz); err != nil {
		return err
	}

	if err := k.RegByAddr.Remove(ctx, addrBz); err != nil {
		return err
	}
	if err := k.Registrations.Remove(ctx, reg.Nullifier); err != nil {
		return err
	}
	if err := k.RegByRegisteredAt.Remove(ctx, collections.Join(reg.RegisteredAt, reg.Nullifier)); err != nil {
		return err
	}
	// These tallies are bumped per registration, so they have to come back down
	// per removal or they drift into counting lifetime registrations rather than
	// live ones.
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
		if err := k.RegCount.Set(ctx, count-1); err != nil {
			return err
		}
	}
	return nil
}

// sweepExpiredRegistrations retires registrations whose validity window has
// closed. Expiry is otherwise only noticed lazily (when the holder or someone
// re-registering trips over it), which means a human who simply stops using the
// chain keeps their vote weight in the human stream indefinitely.
//
// The registered-at index is ordered, so this walks only the lapsed prefix and
// stops at the first live registration rather than scanning the whole set.
func (k Keeper) sweepExpiredRegistrations(ctx context.Context) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	cutoff := now - int64(params.RegistrationValiditySeconds)
	if cutoff <= 0 {
		return nil
	}
	limit := int(params.ExpirySweepLimit)
	if limit <= 0 {
		limit = types.DefaultExpirySweepLimit
	}

	// Collect first: removeRegistration writes to the index being walked.
	var expired []collections.Pair[int64, []byte]
	err = k.RegByRegisteredAt.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		if key.K1() > cutoff {
			return true, nil // ordered index: everything past here is still live
		}
		expired = append(expired, key)
		return len(expired) >= limit, nil
	})
	if err != nil {
		return err
	}
	if len(expired) == 0 {
		return nil
	}

	// Advance once for the whole batch rather than per removal: the settle is to
	// the same block time either way, and every ClearVoter reads the same index.
	if err := k.allocationKeeper.AdvanceIndex(ctx, types.AllocationStream); err != nil {
		return err
	}

	for _, key := range expired {
		reg, err := k.Registrations.Get(ctx, key.K2())
		if err != nil {
			if errors.Is(err, collections.ErrNotFound) {
				// Index entry outlived its registration; drop the orphaned key.
				if err := k.RegByRegisteredAt.Remove(ctx, key); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := k.removeRegistration(ctx, reg); err != nil {
			return err
		}
	}

	// Hitting the cap means more lapsed than one block can retire. That is a
	// safe state — the backlog drains over following blocks — but it is worth
	// surfacing, because a persistent backlog is the signal to raise the limit.
	if len(expired) >= limit {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(
			sdk.NewEvent(
				"registration_sweep_capped",
				sdk.NewAttribute("retired", strconv.Itoa(len(expired))),
				sdk.NewAttribute("limit", strconv.Itoa(limit)),
			),
		)
	}
	return nil
}

// requireValidHuman returns the registration for addr, erroring if it is missing
// or expired.
func (k Keeper) requireValidHuman(ctx context.Context, addr sdk.AccAddress) (types.Registration, error) {
	reg, ok, err := k.getRegistrationByAddr(ctx, addr)
	if err != nil {
		return types.Registration{}, err
	}
	if !ok {
		return types.Registration{}, types.ErrNotRegistered
	}
	expired, err := k.isExpired(ctx, reg)
	if err != nil {
		return types.Registration{}, err
	}
	if expired {
		return types.Registration{}, types.ErrRegExpired
	}
	return reg, nil
}
