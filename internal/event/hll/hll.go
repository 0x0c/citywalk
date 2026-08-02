// Package hll implements the probabilistic distinct-count sketch CW-0009 Unit 5 estimates unique
// reach with, rather than counting exactly. An exact distinct count over an arbitrary slice (a
// message, a variant, a day) requires either retaining every identifier per slice or scanning the
// raw events; a sketch merges across slices (a day into a week) without rescanning, at an error the
// design accepts as well inside what any decision made from a reach number is sensitive to.
package hll

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/bits"
)

// precision is the number of low bits of the hash used to select a register. m = 2^precision
// registers gives a standard error of about 1.04/sqrt(m) ≈ 0.81%, matching CW-0009 Unit 5's "around
// one percent."
const precision = 14

const registerCount = 1 << precision

// alpha is HyperLogLog's bias-correction constant for registerCount registers.
var alpha = 0.7213 / (1 + 1.079/float64(registerCount))

// Sketch is a HyperLogLog register set. The zero value is not usable; construct with New or Unmarshal.
type Sketch struct {
	registers []byte
}

// New returns an empty sketch.
func New() *Sketch {
	return &Sketch{registers: make([]byte, registerCount)}
}

// AddIdentity records identity (a channel ID) as having been seen. The hash is a fast
// non-cryptographic function, per the same reasoning CW-0008 Unit 2 gives for its bucketing hash:
// the requirement is uniformity, not unpredictability, and every identity here is the platform's own
// data, not adversarial input.
func (s *Sketch) AddIdentity(identity string) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(identity)) // hash.Hash.Write never returns an error.
	s.add(h.Sum64())
}

// add records a 64-bit hash: the low precision bits select a register, and the position of the
// lowest set bit among the remaining bits (1-indexed) is the candidate rank, kept only if it exceeds
// the register's current value.
func (s *Sketch) add(hash uint64) {
	idx := hash & (registerCount - 1)
	rest := hash >> precision

	var rank byte
	if rest == 0 {
		// All remaining bits were zero: the rank is capped at the number of bits actually available,
		// same as every standard HyperLogLog implementation.
		rank = byte(64-precision) + 1
	} else {
		rank = byte(bits.TrailingZeros64(rest)) + 1
	}
	if rank > s.registers[idx] {
		s.registers[idx] = rank
	}
}

// Merge folds other into s register-wise (taking the max of each pair), which is what lets a daily
// sketch combine into a weekly one without rescanning the events behind either.
func (s *Sketch) Merge(other *Sketch) error {
	if len(other.registers) != len(s.registers) {
		return fmt.Errorf("hll: merge: register count mismatch (%d vs %d)", len(s.registers), len(other.registers))
	}
	for i, r := range other.registers {
		if r > s.registers[i] {
			s.registers[i] = r
		}
	}
	return nil
}

// Estimate returns the estimated number of distinct identities added across s and everything merged
// into it. Below the standard 2.5m threshold with at least one empty register, linear counting is
// used instead of the raw HyperLogLog estimate — it is markedly more accurate in that range, which is
// exactly where a sparse campaign's reach sketch typically falls.
func (s *Sketch) Estimate() float64 {
	sum := 0.0
	zeros := 0
	for _, r := range s.registers {
		if r == 0 {
			zeros++
		}
		sum += 1.0 / float64(uint64(1)<<r)
	}

	m := float64(registerCount)
	raw := alpha * m * m / sum
	if raw <= 2.5*m && zeros > 0 {
		return m * math.Log(m/float64(zeros))
	}
	return raw
}

// Marshal serializes s for storage (reach_sketch.sketch).
func (s *Sketch) Marshal() []byte {
	out := make([]byte, len(s.registers))
	copy(out, s.registers)
	return out
}

// Unmarshal reconstructs a sketch previously produced by Marshal.
func Unmarshal(b []byte) (*Sketch, error) {
	if len(b) != registerCount {
		return nil, fmt.Errorf("hll: unmarshal: want %d bytes, got %d", registerCount, len(b))
	}
	registers := make([]byte, registerCount)
	copy(registers, b)
	return &Sketch{registers: registers}, nil
}
