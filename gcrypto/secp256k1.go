package gcrypto

import (
	"bytes"
	"context"
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/crypto"
)

// RegisterSecp256k1 registers secp256k1 with the given Registry.
// There is no global registry; it is the caller's responsibility
// to register as needed.
func RegisterSecp256k1(reg *Registry) {
	reg.Register("secp256k", Secp256k1PubKey{}, NewSecp256k1PubKey)
}

type Secp256k1PubKey ecdsa.PublicKey

func NewSecp256k1PubKey(b []byte) (PubKey, error) {
	pubKey, err := crypto.UnmarshalPubkey(b)
	if err != nil {
		return nil, err
	}
	return Secp256k1PubKey(*pubKey), nil
}

func (e Secp256k1PubKey) PubKeyBytes() []byte {
	return crypto.FromECDSAPub((*ecdsa.PublicKey)(&e))
}

func (e Secp256k1PubKey) Verify(msg, sig []byte) bool {
	return crypto.VerifySignature(e.PubKeyBytes(), msg, sig[:len(sig)-1])
}

func (e Secp256k1PubKey) Equal(other PubKey) bool {
	o, ok := other.(Secp256k1PubKey)
	if !ok {
		return false
	}

	return bytes.Equal(e.PubKeyBytes(), o.PubKeyBytes())
}

type Secp256k1Signer struct {
	priv *ecdsa.PrivateKey
	pub  Secp256k1PubKey
}

func NewSecp256k1Signer(priv *ecdsa.PrivateKey) Secp256k1Signer {
	return Secp256k1Signer{
		priv: priv,
		pub:  Secp256k1PubKey(priv.PublicKey),
	}
}

func (s Secp256k1Signer) PubKey() PubKey {
	return s.pub
}

func (s Secp256k1Signer) Sign(_ context.Context, input []byte) ([]byte, error) {
	return crypto.Sign(crypto.Keccak256(input), s.priv)
}
