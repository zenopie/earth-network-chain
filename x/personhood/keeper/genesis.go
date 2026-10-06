package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/earth-network/earth/x/personhood/types"
	"github.com/earth-network/earth/zk/merkle"
	"github.com/earth-network/earth/zk/privacy"
)

// InitGenesis initializes the module's state from a provided genesis state. The
// caretaker stream and its registration-rewards option are seeded by
// x/allocation, which owns them; the splits' voters are too, and only their
// leases are here.
//
// Only the registrations themselves are carried. Every index over them — by
// Document Signer, by issuing country, by registration time, the count, and
// the identity tree — is rebuilt here, so an export cannot ship a set of
// counters or nodes that disagrees with the records they count.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	// The SDK's InitChainer does not validate: a genesis that skipped
	// validate-genesis must not load state the module's Validate refuses
	// (audit D-2).
	if err := genState.Validate(); err != nil {
		return err
	}
	if err := k.Params.Set(ctx, genState.Params); err != nil {
		return err
	}
	if err := k.LastBuyback.Set(ctx, genState.LastBuyback); err != nil {
		return err
	}

	if err := k.IdentitySize.Set(ctx, genState.IdentityTreeSize); err != nil {
		return err
	}
	t, err := k.identityTree(ctx)
	if err != nil {
		return err
	}
	// No record may be dated after genesis (audit 6 B6-3): a registration
	// registered in the future never expires (and registered_at + validity
	// can overflow), and one activated in the future passes every activation
	// bound late. Identity roots below are held to the same.
	genesisTime := sdk.UnwrapSDKContext(ctx).BlockTime().Unix()
	for _, reg := range genState.Registrations {
		if reg.RegisteredAt > genesisTime || reg.ActivatedAt > genesisTime || reg.PredecessorAt > genesisTime {
			return fmt.Errorf("registration %x: registered_at %d, activated_at %d or predecessor_at %d is after genesis time %d",
				reg.Nullifier, reg.RegisteredAt, reg.ActivatedAt, reg.PredecessorAt, genesisTime)
		}
	}
	for _, reg := range genState.Registrations {
		leaf, err := IdentityLeaf(reg.Idc, reg.DscKey, reg.Country, reg.ActivatedAt, reg.PredecessorAt)
		if err != nil {
			return fmt.Errorf("registration %x: %w", reg.Nullifier, err)
		}
		if err := k.PassportsSeen.Set(ctx, reg.Nullifier, reg.Idc); err != nil {
			return err
		}
		if err := t.Update(reg.LeafIndex, leaf); err != nil {
			return err
		}
		if err := k.addRegistration(ctx, reg); err != nil {
			return err
		}
	}
	// Succession leaves are never zeroed: each is rebuilt where it was.
	for _, sc := range genState.Successions {
		leaf, err := SuccessionLeaf(sc.IdcOld, sc.IdcNew)
		if err != nil {
			return fmt.Errorf("succession at %d: %w", sc.LeafIndex, err)
		}
		if err := t.Update(sc.LeafIndex, leaf); err != nil {
			return err
		}
		if err := k.Successions.Set(ctx, sc.LeafIndex, sc); err != nil {
			return err
		}
	}
	// Every identity root record is a membership anchor, so none is taken on
	// faith (audit 4, C3; x/shielded's records are checked the same way): it
	// must be the root of the rebuilt tree at its tree_size, and must not be
	// dated after genesis (a future time keeps it inside the anchor window,
	// and past pruning, for as long as it likes).
	sizes := make(map[uint64]bool, len(genState.IdentityRoots))
	for i, r := range genState.IdentityRoots {
		if r.Time > genesisTime {
			return fmt.Errorf("identity root %d: time %d is after genesis time %d", i, r.Time, genesisTime)
		}
		if r.TreeSize > genState.IdentityTreeSize {
			return fmt.Errorf("identity root %d: tree_size %d is past the tree's %d", i, r.TreeSize, genState.IdentityTreeSize)
		}
		sizes[r.TreeSize] = true
	}
	rebuilt, err := identityRootsAt(genState.Registrations, genState.Successions, sizes)
	if err != nil {
		return err
	}
	for i, r := range genState.IdentityRoots {
		if !bytes.Equal(rebuilt[r.TreeSize], r.Root) {
			return fmt.Errorf("identity root %d (%X) is not the root of the rebuilt tree at size %d", i, r.Root, r.TreeSize)
		}
	}
	for i, r := range genState.IdentityRoots {
		if err := k.putIdentityRoot(ctx, r, i == len(genState.IdentityRoots)-1); err != nil {
			return err
		}
	}
	if err := k.recordIdentityRoot(ctx); err != nil {
		return err
	}

	for _, c := range genState.ClaimNullifiers {
		if err := k.ClaimNullifiers.Set(ctx, collections.Join(c.Day, c.Nullifier)); err != nil {
			return err
		}
	}
	for _, v := range genState.CaretakerVotes {
		if err := k.CaretakerVotes.Set(ctx, v.Nullifier, v.ExpiresAt); err != nil {
			return err
		}
		if err := k.CaretakerExpiry.Set(ctx, collections.Join(v.ExpiresAt, v.Nullifier)); err != nil {
			return err
		}
	}
	if err := k.CaretakerCount.Set(ctx, uint64(len(genState.CaretakerVotes))); err != nil {
		return err
	}
	// A handle's lease runs at most handle_lease_max past genesis: the claim
	// bound (one live handle per passport) assumes no lease is longer
	// (audit 6 B6-3).
	leaseMax := genState.HandleLeaseMax
	if l := genState.Params.HandleLeaseSecondsOrDefault(); l > leaseMax {
		leaseMax = l
	}
	for _, h := range genState.Handles {
		if h.ExpiresAt > genesisTime+leaseMax {
			return fmt.Errorf("handle %q: expires_at %d is past genesis time %d + handle_lease_max %d",
				h.Handle, h.ExpiresAt, genesisTime, leaseMax)
		}
	}
	if err := k.importHandles(ctx, genState.Handles); err != nil {
		return err
	}
	for _, p := range genState.Passports {
		// A passport with a live registration was last registered to it.
		if reg, err := k.Registrations.Get(ctx, p.Nullifier); err == nil && !bytes.Equal(reg.Idc, p.LastIdc) {
			return fmt.Errorf("passport %x: last_idc is not its live registration's idc", p.Nullifier)
		} else if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return err
		}
		if err := k.PassportsSeen.Set(ctx, p.Nullifier, p.LastIdc); err != nil {
			return err
		}
	}
	for _, nf := range genState.HandleMovedOut {
		if err := k.HandleMovedOut.Set(ctx, nf); err != nil {
			return err
		}
	}
	for _, nf := range genState.CaretakerMovedOut {
		if err := k.CaretakerMovedOut.Set(ctx, nf); err != nil {
			return err
		}
	}
	// Validate has checked the set holds every idc the records name.
	for _, idc := range genState.UsedIdcs {
		if err := k.UsedIdcs.Set(ctx, idc); err != nil {
			return err
		}
	}
	if err := k.HandleLeaseMax.Set(ctx, leaseMax); err != nil {
		return err
	}
	for _, u := range genState.UsedBindings {
		if err := k.putUsedBinding(ctx, u.Binding, u.ExpiresAt); err != nil {
			return err
		}
	}
	for _, dsc := range genState.PendingDscPurges {
		if err := k.PendingDscPurge.Set(ctx, dsc); err != nil {
			return err
		}
	}
	for _, r := range genState.DscRates {
		if err := k.DscRate.Set(ctx, r.DscKey, r.Counter); err != nil {
			return err
		}
	}
	for _, r := range genState.CountryRates {
		if err := k.CountryRate.Set(ctx, r.Country, r.Counter); err != nil {
			return err
		}
	}
	if genState.NetworkRate != (types.RateCounter{}) {
		if err := k.NetworkRate.Set(ctx, genState.NetworkRate); err != nil {
			return err
		}
	}
	if genState.LeaseHold != (types.LeaseHold{}) {
		if err := k.LeaseHold.Set(ctx, genState.LeaseHold); err != nil {
			return err
		}
	}
	return nil
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error
	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Deliberately not carried: see last_buyback in genesis.proto.
	genesis.LastBuyback = 0

	if err := k.Registrations.Walk(ctx, nil, func(_ []byte, reg types.Registration) (bool, error) {
		genesis.Registrations = append(genesis.Registrations, reg)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if genesis.IdentityTreeSize, err = k.IdentityTreeSize(ctx); err != nil {
		return nil, err
	}

	latest, err := k.LatestIdentityRoot.Get(ctx)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	var latestRec *types.IdentityRoot
	if err := k.IdentityRootsByTime.Walk(ctx, nil, func(key collections.Pair[int64, []byte]) (bool, error) {
		rec, err := k.IdentityRoots.Get(ctx, key.K2())
		if err != nil {
			return true, err
		}
		if string(rec.Root) == string(latest) {
			latestRec = &rec
		} else {
			genesis.IdentityRoots = append(genesis.IdentityRoots, rec)
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	if latestRec != nil {
		genesis.IdentityRoots = append(genesis.IdentityRoots, *latestRec)
	}
	// Only roots InitGenesis can verify are exported: a root from before a
	// leaf was zeroed (expiry, revocation, a new identity secret) is not the
	// root of the tree rebuilt from the surviving registrations at its size,
	// and the import refuses unverifiable roots. Dropping one drops an anchor
	// only: a proof against it is re-made against the current root.
	sizes := make(map[uint64]bool, len(genesis.IdentityRoots))
	for _, r := range genesis.IdentityRoots {
		if r.TreeSize <= genesis.IdentityTreeSize {
			sizes[r.TreeSize] = true
		}
	}
	if err := k.Successions.Walk(ctx, nil, func(_ uint64, sc types.Succession) (bool, error) {
		genesis.Successions = append(genesis.Successions, sc)
		return false, nil
	}); err != nil {
		return nil, err
	}
	rebuilt, err := identityRootsAt(genesis.Registrations, genesis.Successions, sizes)
	if err != nil {
		return nil, err
	}
	kept := genesis.IdentityRoots[:0]
	for _, r := range genesis.IdentityRoots {
		if bytes.Equal(rebuilt[r.TreeSize], r.Root) {
			kept = append(kept, r)
		}
	}
	genesis.IdentityRoots = kept

	if err := k.ClaimNullifiers.Walk(ctx, nil, func(key collections.Pair[uint64, []byte]) (bool, error) {
		genesis.ClaimNullifiers = append(genesis.ClaimNullifiers, types.ClaimNullifier{Day: key.K1(), Nullifier: key.K2()})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.CaretakerVotes.Walk(ctx, nil, func(nf []byte, expiresAt int64) (bool, error) {
		genesis.CaretakerVotes = append(genesis.CaretakerVotes, types.CaretakerVote{Nullifier: nf, ExpiresAt: expiresAt})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.Handles.Walk(ctx, nil, func(_ string, h types.Handle) (bool, error) {
		genesis.Handles = append(genesis.Handles, h)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.PassportsSeen.Walk(ctx, nil, func(nf, last []byte) (bool, error) {
		genesis.Passports = append(genesis.Passports, types.PassportSeen{Nullifier: nf, LastIdc: last})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.HandleMovedOut.Walk(ctx, nil, func(nf []byte) (bool, error) {
		genesis.HandleMovedOut = append(genesis.HandleMovedOut, nf)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.CaretakerMovedOut.Walk(ctx, nil, func(nf []byte) (bool, error) {
		genesis.CaretakerMovedOut = append(genesis.CaretakerMovedOut, nf)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.UsedIdcs.Walk(ctx, nil, func(idc []byte) (bool, error) {
		genesis.UsedIdcs = append(genesis.UsedIdcs, idc)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if genesis.HandleLeaseMax, err = k.HandleLeaseMax.Get(ctx); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err := k.UsedBindings.Walk(ctx, nil, func(b []byte, until int64) (bool, error) {
		genesis.UsedBindings = append(genesis.UsedBindings, types.UsedRegistrationBinding{Binding: b, ExpiresAt: until})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.PendingDscPurge.Walk(ctx, nil, func(dsc []byte) (bool, error) {
		genesis.PendingDscPurges = append(genesis.PendingDscPurges, dsc)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.DscRate.Walk(ctx, nil, func(dsc []byte, c types.RateCounter) (bool, error) {
		genesis.DscRates = append(genesis.DscRates, types.DscRateCounter{DscKey: dsc, Counter: c})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.CountryRate.Walk(ctx, nil, func(country string, c types.RateCounter) (bool, error) {
		genesis.CountryRates = append(genesis.CountryRates, types.CountryRateCounter{Country: country, Counter: c})
		return false, nil
	}); err != nil {
		return nil, err
	}
	if genesis.NetworkRate, err = k.NetworkRate.Get(ctx); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if genesis.LeaseHold, err = k.LeaseHold.Get(ctx); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	return genesis, nil
}

// identityRootsAt rebuilds the identity tree from regs' leaves (zero at every
// index no registration holds) and returns its root after each size in
// sizes. The tree's root does not depend on its size counter, only on its
// leaves, so the root "at size n" is the root over leaves [0, n).
func identityRootsAt(regs []types.Registration, succs []types.Succession, sizes map[uint64]bool) (map[uint64][]byte, error) {
	out := make(map[uint64][]byte, len(sizes))
	var top uint64
	for n := range sizes {
		if n > top {
			top = n
		}
	}
	leaves := make(map[uint64]fr.Element, len(regs))
	for _, reg := range regs {
		if reg.LeafIndex >= top {
			continue
		}
		leaf, err := IdentityLeaf(reg.Idc, reg.DscKey, reg.Country, reg.ActivatedAt, reg.PredecessorAt)
		if err != nil {
			return nil, fmt.Errorf("registration %x: %w", reg.Nullifier, err)
		}
		leaves[reg.LeafIndex] = leaf
	}
	for _, sc := range succs {
		if sc.LeafIndex >= top {
			continue
		}
		leaf, err := SuccessionLeaf(sc.IdcOld, sc.IdcNew)
		if err != nil {
			return nil, fmt.Errorf("succession at %d: %w", sc.LeafIndex, err)
		}
		leaves[sc.LeafIndex] = leaf
	}
	t := merkle.NewMem()
	record := func() error {
		if !sizes[t.Size()] {
			return nil
		}
		r, err := t.Root()
		if err != nil {
			return err
		}
		out[t.Size()] = privacy.FieldBytes(r)
		return nil
	}
	if err := record(); err != nil {
		return nil, err
	}
	for i := uint64(0); i < top; i++ {
		if _, err := t.Append(leaves[i]); err != nil {
			return nil, err
		}
		if err := record(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
