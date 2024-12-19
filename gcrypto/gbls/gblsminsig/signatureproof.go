package gblsminsig

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gmerkle"
	blst "github.com/supranational/blst/bindings/go"
)

type SignatureProof struct {
	msg []byte

	keys    []PubKey
	keyTree *gmerkle.Tree[PubKey]

	// string(pubkey bytes) -> index in keys
	keyIdxs map[string]int

	keyHash string

	// string(possibly aggregated pubkey bytes) -> validated signature for possibly aggregated key
	sigs map[string][]byte

	// Bits indicating what keys in the keys slice
	// have representation in the sigs map.
	// Note that the sigs map is not necessarily fully aggregated yet;
	// if bits 0 and 1 are set, we may have either separate individual signatures
	// for keys 0 and 1, or we might have the aggregation of 0 and 1.
	sigBitset *bitset.BitSet
}

func NewSignatureProof(msg []byte, trustedKeys []PubKey, pubKeyHash string) (SignatureProof, error) {
	keyTree, err := gmerkle.NewTree(keyAggScheme{}, trustedKeys)
	if err != nil {
		return SignatureProof{}, err
	}

	keyIdxs := make(map[string]int, len(trustedKeys))
	for i, k := range trustedKeys {
		keyIdxs[string(k.PubKeyBytes())] = i
	}

	return SignatureProof{
		msg: msg,

		keys:    trustedKeys,
		keyTree: keyTree,

		keyIdxs: keyIdxs,

		keyHash: pubKeyHash,

		sigs: make(map[string][]byte),

		sigBitset: bitset.New(uint(len(trustedKeys))),
	}, nil
}

func (p SignatureProof) Message() []byte {
	return p.msg
}

func (p SignatureProof) PubKeyHash() []byte {
	return []byte(p.keyHash)
}

// AddSignature adds a signature representing a single key.
//
// This should only be called when receiving the local application's signature for a message.
// Otherwise, use the Merge method to combine incoming proofs with the existing one.
//
// If the signature does not match, or if the public key was not one of the candidate keys,
// an error is returned.
func (p SignatureProof) AddSignature(sig []byte, key gcrypto.PubKey) error {
	pubKeyBytes := string(key.PubKeyBytes())
	i, ok := p.keyIdxs[pubKeyBytes]
	if !ok {
		return fmt.Errorf("unknown key %x", pubKeyBytes)
	}

	pk := p.keys[i]
	if !pk.Verify(p.msg, sig) {
		return errors.New("signature validation failed")
	}

	p.sigs[pubKeyBytes] = sig
	p.sigBitset.Set(uint(i))

	return nil
}

func (p SignatureProof) Matches(other gcrypto.CommonMessageSignatureProof) bool {
	o := other.(SignatureProof)

	if !bytes.Equal(p.msg, o.msg) {
		return false
	}

	// Both the tree and the actual keys should be consistent given a key hash,
	// so only checking the key hash should suffice.
	if p.keyHash != o.keyHash {
		return false
	}

	return true
}

func (p SignatureProof) Merge(other gcrypto.CommonMessageSignatureProof) gcrypto.SignatureProofMergeResult {
	o := other.(SignatureProof)

	if !p.Matches(o) {
		return gcrypto.SignatureProofMergeResult{
			// Zero value has all false fields.
		}
	}

	res := gcrypto.SignatureProofMergeResult{
		// Assume at the beginning that all of other's signatures are valid.
		AllValidSignatures: true,
	}

	// Check if o looks like a strict superset before we modify p.bitset.
	// If both are empty, call this a strict superset.
	// Maybe this is the wrong definition and there is a more appropriate word?
	looksLikeStrictSuperset := (o.sigBitset.None() && p.sigBitset.None()) || o.sigBitset.IsStrictSuperSet(p.sigBitset)

	// We are going to evaluate every incoming signature from other.
	// Our keyTree has already calculated every valid key combination
	// (which is a subset of every *possible* combination).
	for otherKey, otherSig := range o.sigs {
		curSig, ok := p.sigs[otherKey]
		if !ok {
			// We don't have this signature.
			// We are going to evaluate it anyway,
			// even if we have the parent.

			// Rebuild the key from the compressed bytes.
			p2a := new(blst.P2Affine)
			p2a = p2a.Uncompress([]byte(otherKey))
			pk := PubKey(*p2a)

			// Confirm the we know of the reconstituted key.
			if idx, _ := p.keyTree.Lookup(pk); idx < 0 {
				res.AllValidSignatures = false
				continue
			}

			// We know the key; confirm the signature is valid.
			if !pk.Verify(p.msg, []byte(otherSig)) {
				res.AllValidSignatures = false
				continue
			}

			// The signature was valid and we didn't have it, so now add it.
			p.sigs[otherKey] = otherSig
			continue
		}

		// We did have a signature for the key.
		// Confirm their signature is the same.
		if !bytes.Equal(curSig, otherSig) {
			res.AllValidSignatures = false
		}
	}

	res.WasStrictSuperset = looksLikeStrictSuperset && res.AllValidSignatures
	return res
}

func (p SignatureProof) MergeSparse(s gcrypto.SparseSignatureProof) gcrypto.SignatureProofMergeResult {
	if s.PubKeyHash != p.keyHash {
		// Unmergeable.
		return gcrypto.SignatureProofMergeResult{}
	}

	res := gcrypto.SignatureProofMergeResult{
		// Assume all signatures are valid until we encounter an invalid one.
		AllValidSignatures: true,

		// Whether the signatures were increased, or whether we added a strict superset,
		// is determined after iterating over the sparse value.
	}

	addedBS := bitset.New(uint(len(p.keys)))
	bsBefore := p.sigBitset.Clone()

	for _, ss := range s.Signatures {
		var incomingSigBitset bitset.BitSet
		if err := sparseIDToBitset(ss.KeyID, &incomingSigBitset); err != nil {
			res.AllValidSignatures = false
			continue
		}

		if incomingSigBitset.Count() == 0 {
			// Malicious incoming value?
			res.AllValidSignatures = false
			continue
		}

		if incomingSigBitset.Count() != 1 {
			// We can't do this yet, because we don't yet have a clean way
			// to map a bitset into the keyTree.
			panic(errors.New("TODO: handle merging aggregated keys"))
		}

		// But the count=1 case is special because we know that is a leaf, unaggregated key.
		setIdxU, _ := incomingSigBitset.NextSet(0)
		setIdx := int(setIdxU)
		if setIdx >= len(p.keys) || setIdx < 0 {
			res.AllValidSignatures = false
			continue
		}

		pk := p.keys[int(setIdxU)]
		// Do we already have the signature for this key?
		if haveSig, ok := p.sigs[string(pk.PubKeyBytes())]; ok {
			// We already have the signature.
			// Did they send the same bytes we verified earlier?
			if !bytes.Equal(haveSig, ss.Sig) {
				res.AllValidSignatures = false
				continue
			}

			// They sent a matching signature,
			// so mark that signature as added.
			addedBS.Set(setIdxU)
			continue
		}

		// We didn't have the signature.
		// Try adding it directly, which is valid for an unaggregated key.
		if err := p.AddSignature(ss.Sig, pk); err != nil {
			res.AllValidSignatures = false
			continue
		}

		// It added successfully, mark it so.
		addedBS.Set(setIdxU)
	}

	res.IncreasedSignatures = p.sigBitset.Count() > bsBefore.Count()
	res.WasStrictSuperset = addedBS.IsStrictSuperSet(bsBefore)

	return res
}

// HasSparseKeyID reports whether the full proof already contains a signature
// matching the given sparse key ID.
// If the key ID does not properly map into the set of trusted public keys,
// the "valid" return parameter will be false.
func (p SignatureProof) HasSparseKeyID(keyID []byte) (has, valid bool) {
	var check bitset.BitSet
	if err := sparseIDToBitset(keyID, &check); err != nil {
		return false, false
	}

	// TODO: bounds check on the incoming ID,
	// to possibly return early with valid=false.

	return p.sigBitset.IsSuperSet(&check), true
}

func (p SignatureProof) AsSparse() gcrypto.SparseSignatureProof {
	outPubKeys := p.keyTree.BitSetToIDs(p.sigBitset)

	sparseSigs := make([]gcrypto.SparseSignature, len(outPubKeys))

	for i, pubKey := range outPubKeys {
		sig := p.sigs[string(pubKey.PubKeyBytes())]
		// TODO: handle failed lookup by aggregating appropriately,
		// e.g. if we have 0 and 1 and this is looking for 01.

		sparseSigs[i] = gcrypto.SparseSignature{
			KeyID: bitsetToSparseID(p.sigBitset),
			Sig:   sig,
		}
	}

	return gcrypto.SparseSignatureProof{
		PubKeyHash: p.keyHash,
		Signatures: sparseSigs,
	}
}

func (p SignatureProof) Clone() gcrypto.CommonMessageSignatureProof {
	sigs := make(map[string][]byte, len(p.sigs))
	for k, v := range p.sigs {
		sigs[k] = bytes.Clone(v)
	}
	return SignatureProof{
		msg:     bytes.Clone(p.msg),
		keys:    slices.Clone(p.keys),
		keyTree: p.keyTree, // TODO: this needs to be cloned

		keyIdxs: maps.Clone(p.keyIdxs),

		keyHash: p.keyHash,

		sigs: sigs,

		sigBitset: p.sigBitset.Clone(),
	}
}

func (p SignatureProof) Derive() gcrypto.CommonMessageSignatureProof {
	return SignatureProof{
		msg:  bytes.Clone(p.msg),
		keys: slices.Clone(p.keys),

		keyTree: p.keyTree, // TODO: this needs to be cloned

		keyIdxs: maps.Clone(p.keyIdxs),

		keyHash: p.keyHash,

		sigs: make(map[string][]byte),

		sigBitset: bitset.New(uint(len(p.keys))),
	}
}

func (p SignatureProof) SignatureBitSet() *bitset.BitSet {
	return p.sigBitset
}

type keyAggScheme struct{}

func (s keyAggScheme) BranchFactor() uint8 {
	return 2
}

// BranchID aggregates the children.
func (s keyAggScheme) BranchID(depth, rowIdx int, childIDs []PubKey) (PubKey, error) {
	keyAgg := new(blst.P2Aggregate)

	compressed := make([][]byte, len(childIDs))
	for i, c := range childIDs {
		compressed[i] = c.PubKeyBytes()
	}

	if !keyAgg.AggregateCompressed(compressed, true) {
		return PubKey{}, errors.New("failed to aggregate keys")
	}

	aff := keyAgg.ToAffine()
	return PubKey(*aff), nil
}

// LeafID returns the unmodified leafData.
func (s keyAggScheme) LeafID(idx int, leafData PubKey) (PubKey, error) {
	return leafData, nil
}

// bitsetToSparseID returns a byte slice the bits
// representing a possibly aggregated key.
//
// This is an unoptimized implementation;
// we can instead encode two values:
// the first set bit and the run of set bits.
// If we used uint8, we would be limited to 256 validators,
// so encoding this as a pair of uint16 values,
// which will be 32 bits, which is 8 bytes.
// This uses less space than an encoded bitset with more than 32 validators.
//
// Also, this should encode to an existing slice to avoid
// allocating a new slice every time.
func bitsetToSparseID(bs *bitset.BitSet) []byte {
	b, err := bs.MarshalBinary()
	if err != nil {
		panic(fmt.Errorf("failed to encode bitset: %w", err))
	}

	return b
}

func sparseIDToBitset(id []byte, dst *bitset.BitSet) error {
	if err := dst.UnmarshalBinary(id); err != nil {
		return fmt.Errorf("failed to parse sparse ID: %w", err)
	}

	return nil
}
