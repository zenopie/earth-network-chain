package cmd

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	abci "github.com/cometbft/cometbft/abci/types"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/codec"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protowire"

	personhoodkeeper "github.com/earth-network/earth/x/personhood/keeper"
	personhoodtypes "github.com/earth-network/earth/x/personhood/types"
	pkikeeper "github.com/earth-network/earth/x/pki/keeper"
	pkitypes "github.com/earth-network/earth/x/pki/types"
	shieldedkeeper "github.com/earth-network/earth/x/shielded/keeper"
	shieldedtypes "github.com/earth-network/earth/x/shielded/types"
)

// gasCheckCmd runs the chain's own personhood checks against a live node's
// state, without a transaction, for the gas-grant backend.
//
// The backend funds one fee note per passport per month, to a new human whose
// registration the chain would accept (`registration`). (There is no
// transparent grant to an address any more, and no "is this address a human"
// check: nothing on chain links an address to a registration.) The question
// is the chain's to answer, and answering it with a copy of its logic means the copy
// drifts at the next circuit or parameter change. So this builds the real
// personhood and pki keepers over a read-only store whose every read is an
// `abci_query /store/<module>/...` to the node, pinned to one height, and asks
// them. The node does only plain store reads — the proof is verified here, on
// the backend's CPU, so a flood of junk proofs costs the node nothing.
//
// Output is one JSON object on stdout. "ok" false with "error" is the chain
// refusing; a non-zero exit is this command failing to ask (node unreachable),
// which the caller should treat as "try again", not as a refusal.
func gasCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "gas-check",
		Short:        "Run the chain's personhood checks against a node, read-only (for the gas-grant backend)",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("node", "https://rpc.erth.network:443", "CometBFT RPC of the node whose state to read")

	cmd.AddCommand(&cobra.Command{
		Use:   "registration",
		Short: "Would the registration in the MsgRegister on stdin (proto JSON) be accepted? Its fee bundle is not checked (it may be empty). Prints the passport nullifier and whether it is a switch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env, err := newGasCheckEnv(cmd)
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			var msg personhoodtypes.MsgRegister
			if err := env.cdc.UnmarshalJSON(raw, &msg); err != nil {
				return emit(map[string]any{"ok": false, "error": "malformed MsgRegister: " + err.Error()})
			}
			nullifier, switched, err := env.personhood.CheckRegistration(env.ctx, &msg)
			if err != nil {
				return env.refusal(err)
			}
			return emit(map[string]any{"ok": true, "nullifier": hex.EncodeToString(nullifier), "switched": switched, "height": env.height})
		},
	})

	return cmd
}

type gasCheckEnv struct {
	ctx        sdk.Context
	cdc        codec.Codec
	addrCodec  address.Codec
	personhood personhoodkeeper.Keeper
	height     int64
}

func newGasCheckEnv(cmd *cobra.Command) (*gasCheckEnv, error) {
	node, _ := cmd.Flags().GetString("node")
	client, err := rpchttp.New(node, "/websocket")
	if err != nil {
		return nil, err
	}
	bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// Not cancelled on return: the keepers read through it for the rest of the
	// process, which is one check long.
	_ = cancel

	status, err := client.Status(bg)
	if err != nil {
		return nil, fmt.Errorf("node status: %w", err)
	}
	// Every read below is pinned to this height, so the checks see one
	// consistent state rather than whatever each read happens to land on.
	height := status.SyncInfo.LatestBlockHeight
	block, err := client.Block(bg, &height)
	if err != nil {
		return nil, fmt.Errorf("block %d: %w", height, err)
	}

	cdc := gasCheckCodec()
	addrCodec := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	authority := authtypes.NewModuleAddress("gov")

	remote := func(module string) corestore.KVStoreService {
		return remoteStoreService{&remoteKV{ctx: bg, client: client, module: module, height: height}}
	}
	pki := pkikeeper.NewKeeper(remote(pkitypes.StoreKey), cdc, addrCodec, authority)
	// Only its params (the verifying keys) are read: VerifyCircuit.
	shielded := shieldedkeeper.NewKeeper(remote(shieldedtypes.StoreKey), cdc, addrCodec, authority, nil, nil, nil, nil)
	personhood := personhoodkeeper.NewKeeper(remote(personhoodtypes.StoreKey), cdc, addrCodec, authority,
		nil, nil, pki, nil, nil, shielded)

	ctx := sdk.Context{}.
		WithContext(bg).
		WithLogger(log.NewNopLogger()).
		WithBlockHeight(height + 1).
		WithBlockTime(block.Block.Header.Time).
		WithChainID(block.Block.Header.ChainID).
		WithGasMeter(storetypes.NewInfiniteGasMeter()).
		WithEventManager(sdk.NewEventManager())

	return &gasCheckEnv{ctx: ctx, cdc: cdc, addrCodec: addrCodec, personhood: personhood, height: height}, nil
}

// gasCheckCodec decodes what the backend pipes in: a MsgRegister (its fee
// bundle included), as proto JSON.
func gasCheckCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	personhoodtypes.RegisterInterfaces(registry)
	pkitypes.RegisterInterfaces(registry)
	shieldedtypes.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// refusal prints the chain's answer, or fails the command if the answer never
// arrived: a read that did not reach the node is not the chain saying no.
func (e *gasCheckEnv) refusal(err error) error {
	var rerr *remoteReadError
	if errors.As(err, &rerr) {
		return err
	}
	return emit(map[string]any{"ok": false, "error": err.Error(), "height": e.height})
}

func emit(v map[string]any) error {
	return json.NewEncoder(os.Stdout).Encode(v)
}

// --- a read-only KVStore over abci_query ------------------------------------

type remoteReadError struct{ err error }

func (e *remoteReadError) Error() string { return "reading node state: " + e.err.Error() }
func (e *remoteReadError) Unwrap() error { return e.err }

type remoteStoreService struct{ kv *remoteKV }

func (s remoteStoreService) OpenKVStore(context.Context) corestore.KVStore { return s.kv }

type remoteKV struct {
	ctx    context.Context
	client *rpchttp.HTTP
	module string
	height int64
}

func (r *remoteKV) query(path string, data []byte) ([]byte, error) {
	res, err := r.queryFull(path, data, false)
	if err != nil {
		return nil, err
	}
	return res.Value, nil
}

func (r *remoteKV) queryFull(path string, data []byte, prove bool) (*abci.ResponseQuery, error) {
	res, err := r.client.ABCIQueryWithOptions(r.ctx, "/store/"+r.module+path, data,
		rpcclient.ABCIQueryOptions{Height: r.height, Prove: prove})
	if err != nil {
		return nil, &remoteReadError{err}
	}
	if res.Response.Code != 0 {
		return nil, &remoteReadError{fmt.Errorf("code %d: %s", res.Response.Code, res.Response.Log)}
	}
	return &res.Response, nil
}

// Get tells an absent key from a present one with an empty value. abci_query
// answers both with an empty Value, but a KeySet member (a revoked CSCA, a
// spent nullifier, ...) is stored with exactly that empty value: reading it
// as absent would make gas-check blind to it. So an empty answer is asked
// again with a proof, and the proof says which it is.
func (r *remoteKV) Get(key []byte) ([]byte, error) {
	v, err := r.query("/key", key)
	if err != nil {
		return nil, err
	}
	if len(v) > 0 {
		return v, nil
	}
	res, err := r.queryFull("/key", key, true)
	if err != nil {
		return nil, err
	}
	if len(res.Value) > 0 {
		return res.Value, nil
	}
	exists, err := proofSaysExists(res, key)
	if err != nil {
		return nil, &remoteReadError{err}
	}
	if exists {
		return []byte{}, nil
	}
	return nil, nil
}

// proofSaysExists reads the first (store-level) op of an abci_query proof: an
// ics23 CommitmentProof whose oneof is exist = 1 or nonexist = 2. The node is
// trusted here as for every other read; the proof only carries the
// existence bit abci_query's Value cannot.
func proofSaysExists(res *abci.ResponseQuery, key []byte) (bool, error) {
	if res.ProofOps == nil || len(res.ProofOps.Ops) == 0 {
		return false, errors.New("node returned no proof for an empty value")
	}
	b := res.ProofOps.Ops[0].Data
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 || typ != protowire.BytesType {
		return false, errors.New("proof is not an ics23 CommitmentProof")
	}
	msg, m := protowire.ConsumeBytes(b[n:])
	if m < 0 {
		return false, errors.New("proof is truncated")
	}
	switch num {
	case 1: // ExistenceProof{key = 1, value = 2, ...}
		for len(msg) > 0 {
			f, t, k := protowire.ConsumeTag(msg)
			if k < 0 {
				return false, errors.New("existence proof is malformed")
			}
			msg = msg[k:]
			if f == 1 && t == protowire.BytesType {
				pk, l := protowire.ConsumeBytes(msg)
				if l < 0 {
					return false, errors.New("existence proof is truncated")
				}
				if !bytes.Equal(pk, key) {
					return false, errors.New("existence proof is for another key")
				}
				return true, nil
			}
			l := protowire.ConsumeFieldValue(f, t, msg)
			if l < 0 {
				return false, errors.New("existence proof is malformed")
			}
			msg = msg[l:]
		}
		return false, errors.New("existence proof has no key")
	case 2:
		return false, nil
	default:
		return false, fmt.Errorf("unexpected proof kind %d", num)
	}
}

func (r *remoteKV) Has(key []byte) (bool, error) {
	v, err := r.Get(key)
	return v != nil, err
}

func (r *remoteKV) Set([]byte, []byte) error { return errors.New("gas-check store is read-only") }
func (r *remoteKV) Delete([]byte) error      { return errors.New("gas-check store is read-only") }

// Iterator fetches every pair under the longest prefix start and end share —
// the node serves prefixes, not ranges — and trims to [start, end) here. The
// checks only iterate small index ranges (a CSCA's SKI or DN), so the shared
// prefix is at worst one collection.
func (r *remoteKV) Iterator(start, end []byte) (corestore.Iterator, error) {
	return r.iterate(start, end, false)
}

func (r *remoteKV) ReverseIterator(start, end []byte) (corestore.Iterator, error) {
	return r.iterate(start, end, true)
}

func (r *remoteKV) iterate(start, end []byte, reverse bool) (corestore.Iterator, error) {
	prefix := commonPrefix(start, end)
	raw, err := r.query("/subspace", prefix)
	if err != nil {
		return nil, err
	}
	pairs, err := decodePairs(raw)
	if err != nil {
		return nil, &remoteReadError{err}
	}
	var out []pair
	for _, p := range pairs {
		if start != nil && bytes.Compare(p.Key, start) < 0 {
			continue
		}
		if end != nil && bytes.Compare(p.Key, end) >= 0 {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		c := bytes.Compare(out[i].Key, out[j].Key)
		if reverse {
			return c > 0
		}
		return c < 0
	})
	return &sliceIterator{pairs: out, start: start, end: end}, nil
}

type pair struct{ Key, Value []byte }

// decodePairs reads the store's kv.Pairs message — repeated Pair{key = 1,
// value = 2} at field 1 — which lives in an internal package of the store and
// cannot be imported.
func decodePairs(b []byte) ([]pair, error) {
	var out []pair
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || num != 1 || typ != protowire.BytesType {
			return nil, errors.New("subspace response is not kv.Pairs")
		}
		b = b[n:]
		msg, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return nil, errors.New("subspace response is truncated")
		}
		b = b[n:]
		var p pair
		for len(msg) > 0 {
			f, t, m := protowire.ConsumeTag(msg)
			if m < 0 || t != protowire.BytesType {
				return nil, errors.New("kv.Pair is malformed")
			}
			msg = msg[m:]
			v, m := protowire.ConsumeBytes(msg)
			if m < 0 {
				return nil, errors.New("kv.Pair is truncated")
			}
			msg = msg[m:]
			switch f {
			case 1:
				p.Key = v
			case 2:
				p.Value = v
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func commonPrefix(a, b []byte) []byte {
	if b == nil {
		return a
	}
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

type sliceIterator struct {
	pairs      []pair
	i          int
	start, end []byte
}

func (it *sliceIterator) Domain() ([]byte, []byte) { return it.start, it.end }
func (it *sliceIterator) Valid() bool              { return it.i < len(it.pairs) }
func (it *sliceIterator) Next()                    { it.i++ }
func (it *sliceIterator) Key() []byte              { return it.pairs[it.i].Key }
func (it *sliceIterator) Value() []byte            { return it.pairs[it.i].Value }
func (it *sliceIterator) Error() error             { return nil }
func (it *sliceIterator) Close() error             { return nil }
