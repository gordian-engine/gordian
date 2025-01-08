package gblsminsig

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/internal/sigtree"
	blst "github.com/supranational/blst/bindings/go"
)

// SignatureProof is an implementation of [gcrypto.CommonMessageSignatureProof]
// for the BLS keys and signatures in this package.
//
// When extracting sparse signatures from this proof,
// signatures are aggregated pairwise, forming a binary tree.
// If signatures were free to be paired arbitrarily,
// then a validator could receive an aggregation of A-B
// and then a separate aggregation of B-C-D.
// Aggregating them into A-B-B-C-D is valid in general,
// but then you need to either have a way to indicate that B
// has been accounted for twice,
// or you need a way to recover the original signature B
// in order to subtract B to normalize it back to A-B-C-D.
//
// Instead, all validators with the same view of the public keys
// understand how to aggregate keys and signatures in a fixed fashion.
// Arranging the validators such that the leftmost validators are
// the most likely to be online and voting the same way,
// allows the signatures to be more likely aggregated into a single set,
// thereby minimizing bandwidth during consensus gossip.
type SignatureProof struct {
	msg []byte

	sigTree sigtree.Tree

	keyHash string
}

// NewSignatureProof returns a new SignatureProof based on trustedKeys.
//
// The pubKeyHash is sent as part of the sparse signatures,
// and it is meant to ensure that peers agree on the set of keys
// and corresponding signatures.
//
// It may turn out that we need a pair of key hashes --
// one for the real set of ordered validator keys,
// and another hash representing the current arrangement of keys for the proof.
// For instance, if a highly delegated validator has not voted in the past several blocks,
// that validator ought to move towards the end of the list such that
// its absence does not interfere with aggregating the other online validators' signatures.
func NewSignatureProof(msg []byte, trustedKeys []PubKey, pubKeyHash string) (SignatureProof, error) {
	keyIdxs := make(map[string]int, len(trustedKeys))
	for i, k := range trustedKeys {
		keyIdxs[string(k.PubKeyBytes())] = i
	}

	sigTree := sigtree.New(func(yield func(blst.P2Affine) bool) {
		for _, key := range trustedKeys {
			if !yield(blst.P2Affine(key)) {
				return
			}
		}
	}, len(trustedKeys))

	return SignatureProof{
		msg: msg,

		sigTree: sigTree,

		keyHash: pubKeyHash,
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
	pk, ok := key.(PubKey)
	if !ok {
		// Arguably this should panic, but the method is documented to error in this case.
		return fmt.Errorf("expected type gblsminsig.PubKey, got %T", key)
	}

	idx := p.sigTree.Index(blst.P2Affine(pk))
	if idx < 0 {
		return fmt.Errorf("unknown key %x", pk.PubKeyBytes())
	}

	gotSigP1 := new(blst.P1Affine)
	gotSigP1 = gotSigP1.Uncompress(sig)

	// The key is part of the tree.
	// Do we already have the signature?
	if _, haveSigP1, _ := p.sigTree.Get(idx); haveSigP1 != (blst.P1Affine{}) {
		// The signature was non-zero, so now we just compare
		// the incoming signature against that one.
		if !gotSigP1.Equals(&haveSigP1) {
			// Currently not dumping those compressed bytes,
			// because we could get numerous invalid signatures.
			// But we could change this to dump if needed.
			return fmt.Errorf("incoming signature differed from previously verified signature")
		}

		// Otherwise they were already equal, so quit.
		return nil
	}

	// We did not already have the signature, so verify it.
	if !pk.Verify(p.msg, sig) {
		return errors.New("signature verification failed")
	}

	// The signature was verified, so now we can add it.
	p.sigTree.AddSignature(idx, *gotSigP1)

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
	looksLikeStrictSuperset := (o.sigTree.SigBits.None() && p.sigTree.SigBits.None()) ||
		o.sigTree.SigBits.IsStrictSuperSet(p.sigTree.SigBits)

	// We are going to evaluate every incoming signature from other.
	otherIDs := o.sigTree.SparseIndices(nil)
	for _, oID := range otherIDs {
		_, otherSig, _ := o.sigTree.Get(oID)

		haveKey, haveSig, _ := p.sigTree.Get(oID)
		if haveSig == (blst.P1Affine{}) {
			// We didn't have this signature, so we need to verify it.
			if !PubKey(haveKey).Verify(p.msg, otherSig.Compress()) {
				res.AllValidSignatures = false
				continue
			}

			// It verified, so add it to ours.
			countBefore := p.sigTree.SigBits.Count()
			p.sigTree.AddSignature(oID, otherSig)
			if p.sigTree.SigBits.Count() > countBefore {
				// It is possible that this was a signature we had not calculated,
				// but which was not new information.
				res.IncreasedSignatures = true
			}
		} else {
			// We do have the signature; does it match?
			if !haveSig.Equals(&otherSig) {
				res.AllValidSignatures = false
				continue
			}
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

	countBefore := p.sigTree.SigBits.Count()

	for _, ss := range s.Signatures {
		if len(ss.KeyID) != 2 {
			// Maybe this should just return due to the input being malformed?
			res.AllValidSignatures = false
			continue
		}

		id := int(binary.LittleEndian.Uint16(ss.KeyID))
		haveKey, haveSig, ok := p.sigTree.Get(id)
		if !ok {
			res.AllValidSignatures = false
			continue
		}

		if haveSig == (blst.P1Affine{}) {
			// We didn't have this signature, so we need to verify it.
			if !PubKey(haveKey).Verify(p.msg, ss.Sig) {
				res.AllValidSignatures = false
				continue
			}

			// It verified, so add it to ours.
			// Check the count before and after to determine whether this increased our signatures.
			sig := new(blst.P1Affine)
			sig = sig.Uncompress(ss.Sig)
			p.sigTree.AddSignature(id, *sig)
			if p.sigTree.SigBits.Count() > countBefore {
				res.IncreasedSignatures = true
			}
		} else {
			// We did have the signature; does it match?
			sig := new(blst.P1Affine)
			sig = sig.Uncompress(ss.Sig)
			if !haveSig.Equals(sig) {
				res.AllValidSignatures = false
			}
		}
	}

	res.IncreasedSignatures = p.sigTree.SigBits.Count() > countBefore
	// TODO: how to check WasStrictSuperset?
	return res
}

// HasSparseKeyID reports whether the full proof already contains a signature
// matching the given sparse key ID.
// If the key ID does not properly map into the set of trusted public keys,
// the "valid" return parameter will be false.
func (p SignatureProof) HasSparseKeyID(keyID []byte) (has, valid bool) {
	if len(keyID) != 2 {
		return false, false
	}
	id := int(binary.LittleEndian.Uint16(keyID))
	_, sig, ok := p.sigTree.Get(id)
	if !ok {
		return false, false
	}
	return sig != (blst.P1Affine{}), true
}

func (p SignatureProof) AsSparse() gcrypto.SparseSignatureProof {
	ids := p.sigTree.SparseIndices(nil)
	sparseSigs := make([]gcrypto.SparseSignature, len(ids))
	for i, id := range ids {
		_, sig, _ := p.sigTree.Get(id)
		kid := [2]byte{}
		binary.LittleEndian.PutUint16(kid[:], uint16(id))
		sparseSigs[i] = gcrypto.SparseSignature{
			KeyID: kid[:],
			Sig:   sig.Compress(),
		}
	}

	return gcrypto.SparseSignatureProof{
		PubKeyHash: p.keyHash,
		Signatures: sparseSigs,
	}
}

func (p SignatureProof) Clone() gcrypto.CommonMessageSignatureProof {
	return SignatureProof{
		msg:     bytes.Clone(p.msg),
		sigTree: p.sigTree.Clone(),

		keyHash: p.keyHash,
	}
}

func (p SignatureProof) Derive() gcrypto.CommonMessageSignatureProof {
	return SignatureProof{
		msg: bytes.Clone(p.msg),

		sigTree: p.sigTree.Derive(),

		keyHash: p.keyHash,
	}
}

func (p SignatureProof) SignatureBitSet(dst *bitset.BitSet) {
	p.sigTree.SigBits.CopyFull(dst)
}
