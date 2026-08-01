package assign

import "hash/fnv"

// BucketCount is the granularity CW-0008 Unit 2 fixes assignment to: one hundredth of a percent per
// bucket, finer than any split a campaign author writes and coarse enough to keep the range table
// small.
const BucketCount = 10000

// identitySeparator sits between the salt and the identity in the hashed input. A null byte rather
// than a printable character, since neither a salt nor an identity is expected to contain one, which
// keeps ("ab", "c") from hashing identically to ("a", "bc").
const identitySeparator = "\x00"

// Bucket returns salt and identity's assignment bucket, in [0, BucketCount). It is a fast
// non-cryptographic 64-bit hash (FNV-1a) taken modulo BucketCount: Unit 2's requirement is
// uniformity, not unpredictability, and unsigned 64-bit arithmetic throughout means this
// implementation has no negative-modulo case to get wrong — the class of bug Unit 6's parity test
// exists to catch in a signed-integer implementation.
func Bucket(salt, identity string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(salt))
	_, _ = h.Write([]byte(identitySeparator))
	_, _ = h.Write([]byte(identity))
	return int(h.Sum64() % BucketCount)
}
