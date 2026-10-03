package types

// Events. Together they let an indexer rebuild the note tree and the spent
// set from blocks alone: every append emits EventTypeNote with its position,
// in position order; every spend emits EventTypeNullifier; the root recorded
// at the end of a block emits EventTypeRoot. Notes appended by a private tx
// are emitted from the ante, so they appear in the tx's events even when its
// msg later fails.
const (
	EventTypeNote      = "shielded_note"
	EventTypeNullifier = "shielded_nullifier"
	EventTypeRoot      = "shielded_root"
	EventTypeShield    = "shielded_shield"
	EventTypeMint      = "shielded_mint"
	EventTypeUnshield  = "shielded_unshield"
	EventTypeSpend     = "shielded_spend_to_module"
	EventTypeFee       = "shielded_fee"
	EventTypeAsset     = "shielded_asset"

	AttributeKeyPosition   = "position"
	AttributeKeyCommitment = "commitment" // hex
	AttributeKeyCiphertext = "ciphertext" // base64, may be empty
	AttributeKeyNullifier  = "nullifier"  // hex
	AttributeKeyRoot       = "root"       // hex
	AttributeKeyTreeSize   = "tree_size"
	AttributeKeyHeight     = "height"
	AttributeKeyAmount     = "amount"
	AttributeKeySender     = "sender"
	AttributeKeyReceiver   = "receiver"
	AttributeKeyModule     = "module"
	AttributeKeyDenom      = "denom"
	AttributeKeyAssetID    = "asset_id" // hex
	// An open note's opening (MintOpenNote): the chain chose it, and its
	// recipient and amount are public, so it is emitted for the owner to find
	// the note by owner_pk rather than by decrypting.
	AttributeKeyOwnerPK = "owner_pk" // hex
	AttributeKeyRho     = "rho"      // hex
	AttributeKeyRcm     = "rcm"      // hex
)
