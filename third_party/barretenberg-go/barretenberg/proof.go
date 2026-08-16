package barretenberg

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// FieldElementSize is the size of a field element in bytes (256 bits = 32 bytes).
const FieldElementSize = 32

// Proof represents an UltraHonk proof.
type Proof struct {
	raw []byte
}

// ParseProof parses a proof from binary data.
// The data should be in the Barretenberg UltraHonk proof format.
func ParseProof(data []byte) (*Proof, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty data", ErrInvalidProof)
	}

	// Minimum reasonable size for an UltraHonk proof
	const minProofSize = 512
	if len(data) < minProofSize {
		return nil, fmt.Errorf("%w: data too small (%d bytes, minimum %d)", ErrInvalidProof, len(data), minProofSize)
	}

	// Make a copy to ensure immutability
	proofCopy := make([]byte, len(data))
	copy(proofCopy, data)

	return &Proof{raw: proofCopy}, nil
}

// ParseProofHex parses a proof from a hex-encoded string.
func ParseProofHex(hexStr string) (*Proof, error) {
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid hex encoding: %v", ErrInvalidProof, err)
	}
	return ParseProof(data)
}

// Bytes returns a copy of the raw proof bytes.
// The returned slice is safe to modify; it does not alias internal state.
func (p *Proof) Bytes() []byte {
	if p == nil || len(p.raw) == 0 {
		return nil
	}
	result := make([]byte, len(p.raw))
	copy(result, p.raw)
	return result
}

// rawBytes returns the internal proof bytes without copying.
// For use only within the barretenberg package — callers must not modify the slice.
func (p *Proof) rawBytes() []byte {
	if p == nil {
		return nil
	}
	return p.raw
}

// Hex returns the proof as a hex-encoded string.
func (p *Proof) Hex() string {
	if p == nil {
		return ""
	}
	return hex.EncodeToString(p.raw)
}

// Size returns the size of the proof in bytes.
func (p *Proof) Size() int {
	if p == nil {
		return 0
	}
	return len(p.raw)
}

// PublicInputs represents a set of public inputs for proof verification.
type PublicInputs struct {
	values [][]byte // Each value is a 32-byte field element
}

// NewPublicInputs creates a new PublicInputs from field element byte slices.
// Each element must be exactly 32 bytes (256-bit field element in big-endian).
func NewPublicInputs(elements [][]byte) (*PublicInputs, error) {
	if len(elements) == 0 {
		return &PublicInputs{values: nil}, nil
	}

	values := make([][]byte, len(elements))
	for i, elem := range elements {
		if len(elem) != FieldElementSize {
			return nil, fmt.Errorf("%w: element %d has invalid size (%d bytes, expected %d)",
				ErrInvalidPublicInputs, i, len(elem), FieldElementSize)
		}
		values[i] = make([]byte, FieldElementSize)
		copy(values[i], elem)
	}

	return &PublicInputs{values: values}, nil
}

// ParsePublicInputsFromStrings parses public inputs from string representations.
// Each string should be a decimal or hex (0x-prefixed) representation of a field element.
func ParsePublicInputsFromStrings(inputs []string) (*PublicInputs, error) {
	if len(inputs) == 0 {
		return &PublicInputs{values: nil}, nil
	}

	values := make([][]byte, len(inputs))
	for i, input := range inputs {
		elem, err := parseFieldElement(input)
		if err != nil {
			return nil, fmt.Errorf("%w: element %d: %v", ErrInvalidPublicInputs, i, err)
		}
		values[i] = elem
	}

	return &PublicInputs{values: values}, nil
}

// ParsePublicInputsFromHex parses public inputs from hex strings.
// Each string should be a hex representation (with or without 0x prefix) of a 32-byte field element.
func ParsePublicInputsFromHex(hexInputs []string) (*PublicInputs, error) {
	if len(hexInputs) == 0 {
		return &PublicInputs{values: nil}, nil
	}

	values := make([][]byte, len(hexInputs))
	for i, hexStr := range hexInputs {
		elem, err := parseHexFieldElement(hexStr)
		if err != nil {
			return nil, fmt.Errorf("%w: element %d: %v", ErrInvalidPublicInputs, i, err)
		}
		values[i] = elem
	}

	return &PublicInputs{values: values}, nil
}

// Count returns the number of public inputs.
func (pi *PublicInputs) Count() int {
	if pi == nil {
		return 0
	}
	return len(pi.values)
}

// Bytes returns all public inputs concatenated as a single byte slice.
// Each field element is 32 bytes, so the total length is Count() * 32.
func (pi *PublicInputs) Bytes() []byte {
	if pi == nil || len(pi.values) == 0 {
		return nil
	}

	result := make([]byte, 0, len(pi.values)*FieldElementSize)
	for _, v := range pi.values {
		result = append(result, v...)
	}
	return result
}

// rawBytes returns all public inputs concatenated as a single byte slice.
// Unlike Proof.rawBytes(), this still allocates a new buffer (the values are stored
// as separate slices and must be concatenated). It exists only to signal internal-use
// intent; behaviour is identical to Bytes().
func (pi *PublicInputs) rawBytes() []byte {
	return pi.Bytes()
}

// Element returns the i-th public input as a byte slice.
func (pi *PublicInputs) Element(i int) ([]byte, error) {
	if pi == nil {
		return nil, fmt.Errorf("%w: nil PublicInputs", ErrInvalidPublicInputs)
	}
	if i < 0 || i >= len(pi.values) {
		return nil, fmt.Errorf("%w: index %d out of range [0, %d)", ErrInvalidPublicInputs, i, len(pi.values))
	}
	return pi.values[i], nil
}

// parseFieldElement parses a string as a field element.
// Supports decimal numbers and hex numbers (with 0x prefix).
func parseFieldElement(s string) ([]byte, error) {
	if len(s) == 0 {
		return nil, fmt.Errorf("%w: empty string", ErrInvalidFieldElement)
	}

	// Check for hex prefix
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return parseHexFieldElement(s[2:])
	}

	// Parse as decimal
	return parseDecimalFieldElement(s)
}

// parseHexFieldElement parses a hex string as a field element.
func parseHexFieldElement(hexStr string) ([]byte, error) {
	// Remove 0x prefix if present
	if len(hexStr) >= 2 && (hexStr[:2] == "0x" || hexStr[:2] == "0X") {
		hexStr = hexStr[2:]
	}

	// Pad to 64 characters (32 bytes) if shorter
	if len(hexStr) < 64 {
		hexStr = strings.Repeat("0", 64-len(hexStr)) + hexStr
	}

	if len(hexStr) > 64 {
		return nil, fmt.Errorf("%w: hex string too long (%d chars, max 64)", ErrInvalidFieldElement, len(hexStr))
	}

	data, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid hex: %v", ErrInvalidFieldElement, err)
	}

	return data, nil
}

// parseDecimalFieldElement parses a decimal string as a big-endian 32-byte field element.
// Only ASCII digit characters (0-9) are accepted; leading '+' or '-' are rejected.
// Leading zeros are allowed (e.g. "007" == "7"). The input is bounded to prevent
// resource exhaustion on user-controlled input before any big.Int allocation.
func parseDecimalFieldElement(s string) ([]byte, error) {
	// Absolute DoS cap: reject strings longer than 256 chars before allocating.
	// This is generous enough to accommodate any number of leading zeros while
	// still bounding allocations on malicious inputs.
	const maxRawLen = 256
	if len(s) > maxRawLen {
		return nil, fmt.Errorf("%w: decimal string too long (%d chars, max %d)", ErrInvalidFieldElement, len(s), maxRawLen)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("%w: invalid character %q in decimal string", ErrInvalidFieldElement, c)
		}
	}
	// Count significant digits (strip leading zeros) to enforce field-size bound
	// before big.Int allocation.
	sig := strings.TrimLeft(s, "0")
	// BN254 scalar field max (2^254) fits in 77 significant decimal digits.
	const maxSignificantDigits = 78
	if len(sig) > maxSignificantDigits {
		return nil, fmt.Errorf("%w: decimal value too large (%d significant digits, max %d)", ErrInvalidFieldElement, len(sig), maxSignificantDigits)
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("%w: invalid decimal string: %q", ErrInvalidFieldElement, s)
	}
	b := n.Bytes()
	if len(b) > 32 {
		return nil, fmt.Errorf("%w: value exceeds 32-byte field element", ErrInvalidFieldElement)
	}
	var value [32]byte
	copy(value[32-len(b):], b)
	return value[:], nil
}

