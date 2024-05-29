package hyperloglog

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	capacity = uint8(16)
	pp       = uint8(25)
	mp       = uint32(1) << pp
	version  = 1
)

// Sketch is a HyperLogLog data-structure for the count-distinct problem,
// approximating the number of distinct elements in a multiset.
type Sketch struct {
	p          uint8
	b          uint8
	m          uint32
	alpha      float64
	mt         sync.Mutex
	tmpSet     set
	sparseList *compressedList
	regs       *registers
	ResetAt    int64
	isSparse   bool
}

// New returns a HyperLogLog Sketch with 2^14 registers (precision 14)
func New() *Sketch {
	return New14()
}

// New14 returns a HyperLogLog Sketch with 2^14 registers (precision 14)
func New14() *Sketch {
	sk, _ := newSketch(14, true)
	return sk
}

// New16 returns a HyperLogLog Sketch with 2^16 registers (precision 16)
func New16() *Sketch {
	sk, _ := newSketch(16, true)
	return sk
}

// NewNoSparse returns a HyperLogLog Sketch with 2^14 registers (precision 14)
// that will not use a sparse representation
func NewNoSparse() *Sketch {
	sk, _ := newSketch(14, false)
	return sk
}

// New16NoSparse returns a HyperLogLog Sketch with 2^16 registers (precision 16)
// that will not use a sparse representation
func New16NoSparse() *Sketch {
	sk, _ := newSketch(16, false)
	return sk
}

// newSketch returns a HyperLogLog Sketch with 2^precision registers
func newSketch(precision uint8, sparse bool) (*Sketch, error) {
	if precision < 4 || precision > 18 {
		return nil, fmt.Errorf("p has to be >= 4 and <= 18")
	}
	m := uint32(math.Pow(2, float64(precision)))
	s := &Sketch{
		m:        m,
		p:        precision,
		alpha:    alpha(float64(m)),
		mt:       sync.Mutex{},
		ResetAt:  time.Now().Unix(),
		isSparse: sparse,
	}
	if sparse {
		s.tmpSet = set{}
		s.sparseList = newCompressedList(0)
	} else {
		s.regs = newRegisters(m)
	}
	return s, nil
}

func (sk *Sketch) sparse() bool {
	return sk.sparseList != nil
}

// Clone returns a deep copy of sk.
func (sk *Sketch) Clone() *Sketch {
	return &Sketch{
		b:          sk.b,
		p:          sk.p,
		m:          sk.m,
		alpha:      sk.alpha,
		tmpSet:     sk.tmpSet.Clone(),
		sparseList: sk.sparseList.Clone(),
		regs:       sk.regs.clone(),
		mt:         sk.mt,
	}
}

// Converts to normal if the sparse list is too large.
func (sk *Sketch) maybeToNormal() {
	if uint32(len(sk.tmpSet))*100 > sk.m {
		sk.mergeSparse()
		if uint32(sk.sparseList.Len()) > sk.m {
			sk.toNormal()
		}
	}
}

// Merge takes another Sketch and combines it with Sketch h.
// If Sketch h is using the sparse Sketch, it will be converted
// to the normal Sketch.
func (sk *Sketch) Merge(other *Sketch) error {
	if other == nil {
		// Nothing to do
		return nil
	}
	cpOther := other.Clone()

	if sk.p != cpOther.p {
		return errors.New("precisions must be equal")
	}

	if sk.sparse() && other.sparse() {
		for k := range other.tmpSet {
			sk.tmpSet.add(k)
		}
		for iter := other.sparseList.Iter(); iter.HasNext(); {
			sk.tmpSet.add(iter.Next())
		}
		sk.maybeToNormal()
		return nil
	}

	if sk.sparse() {
		sk.toNormal()
	}

	if cpOther.sparse() {
		for k := range cpOther.tmpSet {
			i, r := decodeHash(k, cpOther.p, pp)
			sk.insert(i, r)
		}

		for iter := cpOther.sparseList.Iter(); iter.HasNext(); {
			i, r := decodeHash(iter.Next(), cpOther.p, pp)
			sk.insert(i, r)
		}
	} else {
		if sk.b < cpOther.b {
			sk.regs.rebase(cpOther.b - sk.b)
			sk.b = cpOther.b
		} else {
			cpOther.regs.rebase(sk.b - cpOther.b)
			cpOther.b = sk.b
		}

		for i, v := range cpOther.regs.tailcuts {
			v1 := v.get(0)
			if v1 > sk.regs.get(uint32(i)*2) {
				sk.regs.set(uint32(i)*2, v1)
			}
			v2 := v.get(1)
			if v2 > sk.regs.get(1+uint32(i)*2) {
				sk.regs.set(1+uint32(i)*2, v2)
			}
		}
	}
	return nil
}

// Convert from sparse Sketch to dense Sketch.
func (sk *Sketch) toNormal() {
	if len(sk.tmpSet) > 0 {
		sk.mergeSparse()
	}

	sk.regs = newRegisters(sk.m)
	for iter := sk.sparseList.Iter(); iter.HasNext(); {
		i, r := decodeHash(iter.Next(), sk.p, pp)
		sk.insert(i, r)
	}

	sk.tmpSet = nil
	sk.sparseList = nil
}

func (sk *Sketch) insert(i uint32, r uint8) bool {
	changed := false
	if r-sk.b >= capacity {
		//overflow
		db := sk.regs.min()
		if db > 0 {
			sk.b += db
			sk.regs.rebase(db)
			changed = true
		}
	}
	if r > sk.b {
		val := r - sk.b
		if c1 := capacity - 1; c1 < val {
			val = c1
		}

		if val > sk.regs.get(i) {
			sk.regs.set(i, val)
			changed = true
		}
	}
	return changed
}

// Insert adds element e to sketch
func (sk *Sketch) Insert(e []byte) bool {
	x := hash(e)
	return sk.InsertHash(x)
}

// InsertHash adds hash x to sketch
func (sk *Sketch) InsertHash(x uint64) bool {
	sk.mt.Lock()
	defer sk.mt.Unlock()
	today := time.Now().Unix()
	if sk.ResetAt > today {
		sk, _ = newSketch(sk.p, sk.isSparse)
	}
	if sk.sparse() {
		changed := sk.tmpSet.add(encodeHash(x, sk.p, pp))
		if !changed {
			return false
		}
		if uint32(len(sk.tmpSet))*100 > sk.m/2 {
			sk.mergeSparse()
			if uint32(sk.sparseList.Len()) > sk.m/2 {
				sk.toNormal()
			}
		}
		return true
	} else {
		i, r := getPosVal(x, sk.p)
		return sk.insert(uint32(i), r)
	}
}

// Estimate returns the cardinality of the Sketch
func (sk *Sketch) Estimate() uint64 {
	sk.mt.Lock()
	defer sk.mt.Unlock()
	if sk.sparse() {
		sk.mergeSparse()
		return uint64(linearCount(mp, mp-sk.sparseList.count))
	}

	sum, ez := sk.regs.sumAndZeros(sk.b)
	m := float64(sk.m)
	var est float64

	var beta func(float64) float64
	if sk.p < 16 {
		beta = beta14
	} else {
		beta = beta16
	}

	if sk.b == 0 {
		est = sk.alpha * m * (m - ez) / (sum + beta(ez))
	} else {
		est = sk.alpha * m * m / sum
	}

	return uint64(est + 0.5)
}

func (sk *Sketch) mergeSparse() {
	if len(sk.tmpSet) == 0 {
		return
	}

	keys := make(uint64Slice, 0, len(sk.tmpSet))
	for k := range sk.tmpSet {
		keys = append(keys, k)
	}
	sort.Sort(keys)

	newList := newCompressedList(4*len(sk.tmpSet) + len(sk.sparseList.b))
	for iter, i := sk.sparseList.Iter(), 0; iter.HasNext() || i < len(keys); {
		if !iter.HasNext() {
			newList.Append(keys[i])
			i++
			continue
		}

		if i >= len(keys) {
			newList.Append(iter.Next())
			continue
		}

		x1, x2 := iter.Peek(), keys[i]
		if x1 == x2 {
			newList.Append(iter.Next())
			i++
		} else if x1 > x2 {
			newList.Append(x2)
			i++
		} else {
			newList.Append(iter.Next())
		}
	}

	sk.sparseList = newList
	sk.tmpSet = set{}
}
