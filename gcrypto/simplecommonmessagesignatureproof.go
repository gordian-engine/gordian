package gcrypto

import (
	"bytes"
	"encoding/binary"
	"maps"
	"slices"
	"sort"

	"github.com/bits-and-blooms/bitset"
)

// SimpleCommonMessageSignatureProofScheme is the scheme for a SimpleCommonMessageSignatureProof.
var SimpleCommonMessageSignatureProofScheme CommonMessageSignatureProofScheme = LiteralCommonMessageSignatureProofScheme(
	NewSimpleCommonMessageSignatureProof,
	isValidSimpleCommonSignatureKeyID,
)

// SimpleCommonMessageSignatureProof is the simplest signature proof,
// which only tracks pairs of signatures and public keys.
type SimpleCommonMessageSignatureProof struct {
	msg []byte

	// string(signature bytes) -> signing key
	sigs map[string]PubKey

	// The candidate keys from the call to NewSimpleCommonMessageSignatureProof.
	keys []PubKey

	// string(pub key bytes) -> index in candidateKeys
	keyIdxs map[string]int

	// Indication of the set of candidate keys,
	// so that different proofs can agree that they are comparing
	// against the same public key set.
	keyHash string

	bitset *bitset.BitSet
}

func NewSimpleCommonMessageSignatureProof(msg []byte, candidateKeys []PubKey, pubKeyHash string) (SimpleCommonMessageSignatureProof, error) {
	keyIdxs := make(map[string]int, len(candidateKeys))

	for i, k := range candidateKeys {
		keyIdxs[string(k.PubKeyBytes())] = i
	}

	return SimpleCommonMessageSignatureProof{
		msg:     msg,
		sigs:    make(map[string]PubKey),
		keyIdxs: keyIdxs,

		keys: candidateKeys,

		keyHash: pubKeyHash,

		bitset: bitset.New(uint(len(candidateKeys))),
	}, nil
}

func (p SimpleCommonMessageSignatureProof) Message() []byte {
	return p.msg
}

func (p SimpleCommonMessageSignatureProof) PubKeyHash() []byte {
	return []byte(p.keyHash)
}

func (p SimpleCommonMessageSignatureProof) AddSignature(sig []byte, key PubKey) error {
	keyIdx, ok := p.keyIdxs[string(key.PubKeyBytes())]
	if !ok {
		return ErrUnknownKey
	}
	if !key.Verify(p.msg, sig) {
		return ErrInvalidSignature
	}

	p.sigs[string(sig)] = key
	p.bitset.Set(uint(keyIdx))
	return nil
}

func (p SimpleCommonMessageSignatureProof) Matches(other CommonMessageSignatureProof) bool {
	o := other.(SimpleCommonMessageSignatureProof)

	if !bytes.Equal(p.msg, o.msg) {
		return false
	}

	if p.keyHash != o.keyHash {
		return false
	}

	if !slices.EqualFunc(p.keys, o.keys, func(a, b PubKey) bool {
		return a.Equal(b)
	}) {
		return false
	}

	return true
}

func (p SimpleCommonMessageSignatureProof) Merge(other CommonMessageSignatureProof) SignatureProofMergeResult {
	o := other.(SimpleCommonMessageSignatureProof)

	if !p.Matches(o) {
		return SignatureProofMergeResult{
			// Zero value has all false fields.
		}
	}

	res := SignatureProofMergeResult{
		// Assume at the beginning that all of other's signatures are valid.
		AllValidSignatures: true,
	}

	// Check if o looks like a strict superset before we modify p.bitset.
	// If both are empty, call this a strict superset.
	// Maybe this is the wrong definition and there is a more appropriate word?
	looksLikeStrictSuperset := (o.bitset.None() && p.bitset.None()) || o.bitset.IsStrictSuperSet(p.bitset)

	// We trust the current signatures, but we will still check the other's.
	for otherSig, otherKey := range o.sigs {
		curKey, ok := p.sigs[otherSig]
		if !ok {
			// We didn't have this signature.
			// But we know we do have the key because of the earlier Matches check.
			// If we can add it successfully then it was valid.
			if err := p.AddSignature([]byte(otherSig), otherKey); err == nil {
				res.IncreasedSignatures = true
			} else {
				res.AllValidSignatures = false
			}

			continue
		}

		// Otherwise we did have the current signature.
		// It must be associated with the same key.
		if !curKey.Equal(otherKey) {
			res.AllValidSignatures = false
		}
	}

	res.WasStrictSuperset = looksLikeStrictSuperset && res.AllValidSignatures
	return res
}

func (p SimpleCommonMessageSignatureProof) Clone() CommonMessageSignatureProof {
	return SimpleCommonMessageSignatureProof{
		msg: bytes.Clone(p.msg),

		sigs: maps.Clone(p.sigs), // Okay to have new references to same public key values.

		keys: p.keys,

		keyHash: p.keyHash,

		keyIdxs: maps.Clone(p.keyIdxs),

		bitset: p.bitset.Clone(),
	}
}

func (p SimpleCommonMessageSignatureProof) Derive() CommonMessageSignatureProof {
	return SimpleCommonMessageSignatureProof{
		msg:     bytes.Clone(p.msg),
		sigs:    make(map[string]PubKey),
		keys:    p.keys,
		keyHash: p.keyHash,
		keyIdxs: maps.Clone(p.keyIdxs),

		bitset: bitset.New(uint(len(p.keys))),
	}
}

func (p SimpleCommonMessageSignatureProof) SignatureBitSet(dst *bitset.BitSet) {
	p.bitset.CopyFull(dst)
}

func (p SimpleCommonMessageSignatureProof) AsSparse() SparseSignatureProof {
	sparseSigs := make([]SparseSignature, 0, len(p.sigs))
	for sigBytes, pubKey := range p.sigs {
		keyIdx := p.keyIdxs[string(pubKey.PubKeyBytes())]

		b := [2]byte{}
		binary.BigEndian.PutUint16(b[:], uint16(keyIdx))

		sparseSigs = append(sparseSigs, SparseSignature{
			KeyID: b[:],
			Sig:   []byte(sigBytes),
		})
	}

	// Ensure the outgoing signatures are always in key-sorted order.
	// No key IDs should be duplicated, so this doesn't need a stable sort.
	sort.Slice(sparseSigs, func(i, j int) bool {
		return bytes.Compare(sparseSigs[i].KeyID, sparseSigs[j].KeyID) < 0
	})

	return SparseSignatureProof{
		PubKeyHash: p.keyHash,

		Signatures: sparseSigs,
	}
}

func (p SimpleCommonMessageSignatureProof) MergeSparse(s SparseSignatureProof) SignatureProofMergeResult {
	if p.keyHash != s.PubKeyHash {
		return SignatureProofMergeResult{}
	}

	res := SignatureProofMergeResult{
		// Assume all signatures are valid until we encounter an invalid one.
		AllValidSignatures: true,

		// Whether the signatures were increased, or whether we added a strict superset,
		// is determined after iterating over the sparse value.
	}

	addedBS := bitset.New(uint(len(p.keys)))
	bsBefore := p.bitset.Clone()

	for _, sparseSig := range s.Signatures {
		// Assuming the index can be represented in a 16 bit integer.
		// This type is certainly not intended to support 32k public keys.
		n := int(binary.BigEndian.Uint16(sparseSig.KeyID))

		if n < 0 || n >= len(p.keys) {
			res.AllValidSignatures = false
			continue
		}
		key := p.keys[n]

		if err := p.AddSignature(sparseSig.Sig, key); err != nil {
			res.AllValidSignatures = false
			continue
		}

		addedBS.Set(uint(n))
	}
	if p.bitset.Count() > bsBefore.Count() {
		res.IncreasedSignatures = true
	}

	res.WasStrictSuperset = addedBS.IsStrictSuperSet(bsBefore)

	return res
}

func (p SimpleCommonMessageSignatureProof) HasSparseKeyID(keyID []byte) (has, valid bool) {
	if len(keyID) != 2 {
		// Invalid because the key IDs must be a big endian uint16.
		return false, false
	}

	u := binary.BigEndian.Uint16(keyID)
	idx := int(u)
	if idx < 0 || idx >= len(p.keys) {
		// Key ID must be in range to be valid.
		return false, false
	}

	// Now we know idx is in range.
	// Do we actually have a signature to go with it?
	has = p.bitset.Test(uint(u))
	return has, true
}

func isValidSimpleCommonSignatureKeyID(id []byte, keys []PubKey) bool {
	if len(id) != 2 {
		// Invalid because the key IDs must be a big endian uint16.
		return false
	}

	u := binary.BigEndian.Uint16(id)
	idx := int(u)
	if idx < 0 || idx >= len(keys) {
		// Key ID must be in range to be valid.
		return false
	}

	// ID is in range so it is valid.
	return true
}
