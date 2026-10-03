package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/earth-network/earth/x/personhood/types"
)

// Referral codes (types.ReferralCode): a human-readable name for a referrer
// binding, claimed with MsgBindReferrer and named by a registration instead
// of the address (MsgRegister.affiliate_code).
//
// A nullifier has at most one ACTIVE code (ReferralCodeByNf), the one a
// registration naming it resolves through. Every record names the
// nullifier that holds or last held it and releases_at: while the binding
// is live that is its expiry plus the grace period, refreshed with the
// binding; when the binding is cleared, or moves to another code, it is
// that moment plus the grace period. Until releases_at only the same
// nullifier may claim the code; after it anyone may, and the sweep deletes
// the record.

// codeAvailable refuses code if another nullifier holds or reserves it.
func (k Keeper) codeAvailable(ctx context.Context, nf []byte, code string) error {
	rec, err := k.ReferralCodes.Get(ctx, code)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if string(rec.Nullifier) == string(nf) || rec.ReleasesAt <= sdk.UnwrapSDKContext(ctx).BlockTime().Unix() {
		return nil
	}
	return types.ErrReferralCodeTaken.Wrapf("%q is held until %d", code, rec.ReleasesAt)
}

// setCodeRecord writes rec and its release index entry, replacing an older
// entry for the same code.
func (k Keeper) setCodeRecord(ctx context.Context, rec types.ReferralCode) error {
	if old, err := k.ReferralCodes.Get(ctx, rec.Code); err == nil {
		if err := k.ReferralCodeRelease.Remove(ctx, collections.Join(old.ReleasesAt, rec.Code)); err != nil {
			return err
		}
		if string(old.Nullifier) != string(rec.Nullifier) {
			// Taken over after its release: the old holder loses it.
			if cur, err := k.ReferralCodeByNf.Get(ctx, old.Nullifier); err == nil && cur == rec.Code {
				if err := k.ReferralCodeByNf.Remove(ctx, old.Nullifier); err != nil {
					return err
				}
			}
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.ReferralCodes.Set(ctx, rec.Code, rec); err != nil {
		return err
	}
	return k.ReferralCodeRelease.Set(ctx, collections.Join(rec.ReleasesAt, rec.Code))
}

// releaseCode starts code's grace period now (it stays reserved to its
// holder until then).
func (k Keeper) releaseCode(ctx context.Context, code string) error {
	rec, err := k.ReferralCodes.Get(ctx, code)
	if errors.Is(err, collections.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	rec.ReleasesAt = sdk.UnwrapSDKContext(ctx).BlockTime().Unix() + types.ReferralCodeGraceSeconds
	return k.setCodeRecord(ctx, rec)
}

// bindCode makes code (or, if empty, nf's current active code, if any) nf's
// active code, held until expiresAt plus the grace period. It returns the
// code bound ("" for none). A different previous active code is released.
func (k Keeper) bindCode(ctx context.Context, nf []byte, code string, expiresAt int64) (string, error) {
	cur, err := k.ReferralCodeByNf.Get(ctx, nf)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return "", err
	}
	if code == "" {
		code = cur
		if code == "" {
			return "", nil
		}
		// The kept code may have been released and taken over meanwhile.
		if rec, err := k.ReferralCodes.Get(ctx, code); err != nil || string(rec.Nullifier) != string(nf) {
			if err := k.ReferralCodeByNf.Remove(ctx, nf); err != nil {
				return "", err
			}
			return "", nil
		}
	}
	if err := k.codeAvailable(ctx, nf, code); err != nil {
		return "", err
	}
	if cur != "" && cur != code {
		if err := k.releaseCode(ctx, cur); err != nil {
			return "", err
		}
	}
	if err := k.setCodeRecord(ctx, types.ReferralCode{
		Code: code, Nullifier: nf, ReleasesAt: expiresAt + types.ReferralCodeGraceSeconds,
	}); err != nil {
		return "", err
	}
	return code, k.ReferralCodeByNf.Set(ctx, nf, code)
}

// resolveReferralCode returns the address code names: the address of the
// referrer binding whose active code it is, if that binding is live.
func (k Keeper) resolveReferralCode(ctx context.Context, code string) (addr []byte, live bool, expiresAt int64, err error) {
	rec, err := k.ReferralCodes.Get(ctx, code)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, false, 0, nil
	} else if err != nil {
		return nil, false, 0, err
	}
	if cur, err := k.ReferralCodeByNf.Get(ctx, rec.Nullifier); errors.Is(err, collections.ErrNotFound) || (err == nil && cur != code) {
		return nil, false, 0, nil
	} else if err != nil {
		return nil, false, 0, err
	}
	b, err := k.ReferrerBindings.Get(ctx, rec.Nullifier)
	if errors.Is(err, collections.ErrNotFound) {
		return nil, false, 0, nil
	} else if err != nil {
		return nil, false, 0, err
	}
	if addr, err = k.addressCodec.StringToBytes(b.Address); err != nil {
		return nil, false, 0, err
	}
	return addr, b.ExpiresAt > sdk.UnwrapSDKContext(ctx).BlockTime().Unix(), b.ExpiresAt, nil
}

// sweepReferralCodes deletes up to budget codes past their release.
func (k Keeper) sweepReferralCodes(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	now := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	var due []collections.Pair[int64, string]
	if err := k.ReferralCodeRelease.Walk(ctx, nil, func(key collections.Pair[int64, string]) (bool, error) {
		if key.K1() > now {
			return true, nil
		}
		due = append(due, key)
		return len(due) >= budget, nil
	}); err != nil {
		return 0, err
	}
	for _, key := range due {
		if err := k.ReferralCodeRelease.Remove(ctx, key); err != nil {
			return 0, err
		}
		rec, err := k.ReferralCodes.Get(ctx, key.K2())
		if errors.Is(err, collections.ErrNotFound) {
			continue
		} else if err != nil {
			return 0, err
		}
		if rec.ReleasesAt != key.K1() {
			continue // refreshed: a later entry is its own
		}
		if err := k.ReferralCodes.Remove(ctx, rec.Code); err != nil {
			return 0, err
		}
		if cur, err := k.ReferralCodeByNf.Get(ctx, rec.Nullifier); err == nil && cur == rec.Code {
			if err := k.ReferralCodeByNf.Remove(ctx, rec.Nullifier); err != nil {
				return 0, err
			}
		}
	}
	return len(due), nil
}

// importReferralCodes loads genesis codes; each nullifier's active code is
// its record with the latest release (types.validateReferralCodes refuses
// ties).
func (k Keeper) importReferralCodes(ctx context.Context, codes []types.ReferralCode) error {
	latest := map[string]types.ReferralCode{}
	for _, c := range codes {
		if err := k.setCodeRecord(ctx, c); err != nil {
			return err
		}
		if l, ok := latest[string(c.Nullifier)]; !ok || c.ReleasesAt > l.ReleasesAt {
			latest[string(c.Nullifier)] = c
		}
	}
	for nf, c := range latest {
		if err := k.ReferralCodeByNf.Set(ctx, []byte(nf), c.Code); err != nil {
			return err
		}
	}
	return nil
}

// ReferrerByCode implements the query.
func (q queryServer) ReferrerByCode(ctx context.Context, req *types.QueryReferrerByCodeRequest) (*types.QueryReferrerByCodeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	res := &types.QueryReferrerByCodeResponse{}
	if rec, err := q.k.ReferralCodes.Get(ctx, req.Code); err == nil {
		res.ReleasesAt = rec.ReleasesAt
	} else if !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	addr, live, expiresAt, err := q.k.resolveReferralCode(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	if addr != nil {
		if res.Address, err = q.k.addressCodec.BytesToString(addr); err != nil {
			return nil, err
		}
	}
	res.Live, res.ExpiresAt = live, expiresAt
	return res, nil
}
