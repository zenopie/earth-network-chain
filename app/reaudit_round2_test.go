package app

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/require"
)

// Re-audit R7 ("one tx encoding"): a relayer re-spelling an unsigned private
// tx without touching anything the sighash binds (a deprecated AuthInfo.tip,
// a non-critical unknown field appended to the body, one inside the msg) is
// refused, in CheckTx and in a block. Only the canonical encoding lands.
func TestPrivateTxRespellingsRefused(t *testing.T) {
	e := initDexEnv(t)
	e.shield(uint64(10_000 * ssErth))
	e.shield(uint64(100 * ssErth))
	erth := e.w.unspent("uerth", uint64(1_000*ssErth))
	anml, _ := e.noteSwap(erth, uint64(1_000*ssErth), "uanml", 1, 0)
	_ = e.buildLegs(ssFee, leg{anml, 5})
	in := e.w.unspent("uerth", uint64(ssErth))
	dm, _, _ := e.delegateMsg(e.genesisValidator(), in, uint64(ssErth))
	orig := e.privateTx(dm)

	var raw txtypes.TxRaw
	require.NoError(t, raw.Unmarshal(orig))
	var ai txtypes.AuthInfo
	require.NoError(t, ai.Unmarshal(raw.AuthInfoBytes))
	ai.Tip = &txtypes.Tip{Tipper: "x"} //nolint:staticcheck
	tipped := raw
	tipped.AuthInfoBytes, _ = ai.Marshal()
	tbz, _ := tipped.Marshal()

	// field 1025, varint: tag (1025<<3)|0 = 0x88 0x40, value 1
	padded := raw
	padded.BodyBytes = append(append([]byte(nil), raw.BodyBytes...), 0x88, 0x40, 0x01)
	pbz, _ := padded.Marshal()

	// the same unknown field inside the msg's Any value
	var body txtypes.TxBody
	require.NoError(t, body.Unmarshal(raw.BodyBytes))
	body.Messages[0].Value = append(append([]byte(nil), body.Messages[0].Value...), 0x88, 0x40, 0x01)
	inner := raw
	inner.BodyBytes, _ = body.Marshal()
	ibz, _ := inner.Marshal()

	for name, bz := range map[string][]byte{"tip": tbz, "body-noncritical": pbz, "msg-noncritical": ibz} {
		res := e.checkTx(bz)
		require.NotZero(t, res.Code, "%s: %s", name, res.Log)
		require.Contains(t, []uint32{sdkerrors.ErrInvalidRequest.ABCICode(), sdkerrors.ErrTxDecode.ABCICode()}, res.Code, "%s: %s", name, res.Log)
		r := e.run(bz)
		require.NotZero(t, r.Code, "%s delivered: %s", name, r.Log)
	}
	res := e.checkTx(orig)
	require.Zero(t, res.Code, res.Log)
	r := e.run(orig)
	require.Zero(t, r.Code, r.Log)
}
