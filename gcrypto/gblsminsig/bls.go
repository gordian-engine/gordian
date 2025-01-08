package gblsminsig

import (
	"context"
	"errors"
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	blst "github.com/supranational/blst/bindings/go"
)

const keyTypeName = "bls-minsig"

// The domain separation tag is a requirement per RFC9380 (Hashing to Elliptic Curves).
// See sections 2.2.5 (domain separation),
// 3.1 (domain separation requirements),
// and 8.10 (suite ID naming conventions).
//
// Furthermore, see also draft-irtf-cfrg-bls-signature-05,
// section 4.1 (ciphersuite format),
// as that is the actual format being followed here.
//
// The ciphersuite ID according to the BLS signature document is:
//
//	"BLS_SIG_" || H2C_SUITE_ID || SC_TAG || "_"
//
// And the H2C_SUITE_ID, per RFC9380 section 8.8.1, is:
//
//	BLS12381G1_XMD:SHA-256_SSWU_RO_
//
// Which only leaves the SC_TAG value, which is "NUL" for the basic scheme.
var DomainSeparationTag = []byte("BLS_SIG_BLS12381G1_XMD:SHA-256_SSWU_RO_NUL_")

// Register registers the BLS minimzed-signature key type with the given Registry.
func Register(reg *gcrypto.Registry) {
	reg.Register(keyTypeName, PubKey{}, NewPubKey)
}

// PubKey wraps a blst.P2Affine and defines methods for the [gcrypto.PubKey] interface.
type PubKey blst.P2Affine

// NewPubKey decodes a compressed p2 affine point
// and returns the public key for it.
func NewPubKey(b []byte) (gcrypto.PubKey, error) {
	// This is checked inside Uncompress too,
	// but checking it here is an opportunity to return a more meaningful error.
	if len(b) != blst.BLST_P2_COMPRESS_BYTES {
		return nil, fmt.Errorf("expected %d compressed bytes, got %d", blst.BLST_P2_COMPRESS_BYTES, len(b))
	}

	p2a := new(blst.P2Affine)
	p2a = p2a.Uncompress(b)

	if p2a == nil {
		return nil, errors.New("failed to decompress input")
	}

	if !p2a.KeyValidate() {
		return nil, errors.New("input key failed validation")
	}

	pk := PubKey(*p2a)
	return pk, nil
}

// Equal reports whether other is the same public key as k.
func (k PubKey) Equal(other gcrypto.PubKey) bool {
	o, ok := other.(PubKey)
	if !ok {
		return false
	}

	p2a := blst.P2Affine(k)

	p2o := blst.P2Affine(o)
	return p2a.Equals(&p2o)
}

// PubKeyBytes returns the compressed bytes underlying k's P2 affine point.
func (k PubKey) PubKeyBytes() []byte {
	p2a := blst.P2Affine(k)
	return p2a.Compress()
}

// Verify reports whether sig matches k for msg.
func (k PubKey) Verify(msg, sig []byte) bool {
	// Signature is P1, and we assume the signature is compressed.
	p1a := new(blst.P1Affine)
	p1a = p1a.Uncompress(sig)
	if p1a == nil {
		return false
	}

	// Unclear if false is the correct input here.
	if !p1a.SigValidate(false) {
		return false
	}

	// Cast the public key back to p2,
	// so we can verify it against the p1 signature.
	p2a := blst.P2Affine(k)

	return p1a.Verify(false, &p2a, false, blst.Message(msg), DomainSeparationTag)
}

// TypeName returns the type name for minimized-signature BLS signatures.
func (k PubKey) TypeName() string {
	return keyTypeName
}

// Signer satisfies [gcrypto.Signer] for minimized-signature BLS.
type Signer struct {
	// The secret is a scalar,
	// but the blst package aliases it as SecretKey
	// to add a few more methods.
	secret blst.SecretKey

	// The point is the effective public key.
	// The point on its own is insufficient to derive the secret.
	point blst.P2Affine
}

// NewSigner returns a new signer.
// The initial key material must be at least 32 bytes,
// and should be cryptographically random.
func NewSigner(ikm []byte) (Signer, error) {
	if len(ikm) < blst.BLST_SCALAR_BYTES {
		return Signer{}, fmt.Errorf(
			"ikm data too short: got %d, need at least %d",
			len(ikm), blst.BLST_SCALAR_BYTES,
		)
	}
	salt := []byte("TODO") // Need to decide how to get the salt configurable.
	secretKey := blst.KeyGenV5(ikm, salt)

	point := new(blst.P2Affine)
	point = point.From(secretKey)

	return Signer{
		secret: *secretKey,
		point:  *point,
	}, nil
}

// PubKey returns the [PubKey] for s
// (which is actually the p2 point).
func (s Signer) PubKey() gcrypto.PubKey {
	return PubKey(s.point)
}

// Sign produces the signed point for the given input.
//
// It uses the [DomainSeparationTag],
// which must be provided to verification too.
// The [PubKey] type in this package is hardcoded to use the same DST.
func (s Signer) Sign(_ context.Context, input []byte) ([]byte, error) {
	sig := new(blst.P1Affine).Sign(&s.secret, input, DomainSeparationTag, true)

	// sig could be nil only if option parsing failed.
	if sig == nil {
		return nil, errors.New("failed to sign")
	}

	// The signature is a new point on the p1 affine curve.
	return sig.Compress(), nil
}
