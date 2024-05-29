package hyperloglog

import (
	"math/bits"
	"sync"
)

func getIndex(k uint32, p, pp uint8) uint32 {
	if k&1 == 1 {
		return bextr32(k, 32-p, p)
	}
	return bextr32(k, pp-p+1, p)
}

// Encode a hash to be used in the sparse representation.
func encodeHash(x uint64, p, pp uint8) uint32 {
	idx := uint32(bextr(x, 64-pp, pp))
	if bextr(x, 64-pp, pp-p) == 0 {
		zeros := bits.LeadingZeros64((bextr(x, 0, 64-pp)<<pp)|(1<<pp-1)) + 1
		return idx<<7 | uint32(zeros<<1) | 1
	}
	return idx << 1
}

// Decode a hash from the sparse representation.
func decodeHash(k uint32, p, pp uint8) (uint32, uint8) {
	var r uint8
	if k&1 == 1 {
		r = uint8(bextr32(k, 1, 6)) + pp - p
	} else {
		// We can use the 64bit clz implementation and reduce the result
		// by 32 to get a clz for a 32bit word.
		r = uint8(bits.LeadingZeros64(uint64(k<<(32-pp+p-1))) - 31) // -32 + 1
	}
	return getIndex(k, p, pp), r
}

type set struct {
	M  map[uint32]struct{}
	Mt sync.RWMutex
}

func (s set) add(v uint32) bool {
	s.Mt.Lock()
	defer s.Mt.Unlock()
	if _, ok := s.M[v]; ok {
		return false
	}
	s.M[v] = struct{}{}
	return true
}

func (s set) Clone() *set {
	if s.M == nil {
		return nil
	}

	newS := make(map[uint32]struct{}, len(s.M))
	for k, v := range s.M {
		newS[k] = v
	}
	return &set{Mt: s.Mt, M: newS}
}

func (s set) MarshalBinary() (data []byte, err error) {
	// 4 bytes for the size of the set, and 4 bytes for each key.
	// list.
	data = make([]byte, 0, 4+(4*len(s.M)))

	// Length of the set. We only need 32 bits because the size of the set
	// couldn't exceed that on 32 bit architectures.
	sl := len(s.M)
	data = append(data, []byte{
		byte(sl >> 24),
		byte(sl >> 16),
		byte(sl >> 8),
		byte(sl),
	}...)

	// Marshal each element in the set.
	for k := range s.M {
		data = append(data, []byte{
			byte(k >> 24),
			byte(k >> 16),
			byte(k >> 8),
			byte(k),
		}...)
	}

	return data, nil
}

type uint64Slice []uint32

func (p uint64Slice) Len() int           { return len(p) }
func (p uint64Slice) Less(i, j int) bool { return p[i] < p[j] }
func (p uint64Slice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
