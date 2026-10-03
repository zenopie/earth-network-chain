package indexed

import (
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// MemIndex is a sorted in-memory Index. It never errors.
type MemIndex struct {
	vals []fr.Element // ascending
	idx  []uint64
}

var _ Index = (*MemIndex)(nil)

// NewMemIndex is an empty MemIndex.
func NewMemIndex() *MemIndex { return &MemIndex{} }

// search is the first position whose value is >= v.
func (m *MemIndex) search(v fr.Element) int {
	return sort.Search(len(m.vals), func(i int) bool { return m.vals[i].Cmp(&v) >= 0 })
}

func (m *MemIndex) Get(v fr.Element) (uint64, bool, error) {
	i := m.search(v)
	if i < len(m.vals) && m.vals[i] == v {
		return m.idx[i], true, nil
	}
	return 0, false, nil
}

func (m *MemIndex) Below(v fr.Element) (fr.Element, uint64, bool, error) {
	i := m.search(v)
	if i == 0 {
		return fr.Element{}, 0, false, nil
	}
	return m.vals[i-1], m.idx[i-1], true, nil
}

func (m *MemIndex) Above(v fr.Element) (fr.Element, uint64, bool, error) {
	i := m.search(v)
	if i < len(m.vals) && m.vals[i] == v {
		i++
	}
	if i >= len(m.vals) {
		return fr.Element{}, 0, false, nil
	}
	return m.vals[i], m.idx[i], true, nil
}

func (m *MemIndex) Put(v fr.Element, index uint64) error {
	i := m.search(v)
	m.vals = append(m.vals, fr.Element{})
	m.idx = append(m.idx, 0)
	copy(m.vals[i+1:], m.vals[i:])
	copy(m.idx[i+1:], m.idx[i:])
	m.vals[i], m.idx[i] = v, index
	return nil
}
