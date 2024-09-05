package gcrypto

import (
	"context"
	"crypto"
	"crypto/ed25519"
)

// RegisterEd25519 registers ed25519 with the given Registry.
// There is no global registery; it is the caller's responsibility
// to register as needed.
func RegisterEd25519(reg *Registry) {
	reg.Register("ed25519", Ed25519PubKey{}, NewEd25519PubKey)
}

type Ed25519PubKey ed25519.PublicKey

func NewEd25519PubKey(b []byte) (PubKey, error) {
	return Ed25519PubKey(b), nil
}

func (e Ed25519PubKey) PubKeyBytes() []byte {
	return []byte(e)
}

func (e Ed25519PubKey) Verify(msg, sig []byte) bool {
	return ed25519.Verify(ed25519.PublicKey(e), msg, sig)
}

func (e Ed25519PubKey) Equal(other PubKey) bool {
	o, ok := other.(Ed25519PubKey)
	if !ok {
		return false
	}

	return ed25519.PublicKey(e).Equal(ed25519.PublicKey(o))
}

// Address implements PubKey.
func (e Ed25519PubKey) Address() []byte {
	return e.PubKeyBytes()
}

type Ed25519Signer struct {
	priv ed25519.PrivateKey
	pub  Ed25519PubKey
}

func NewEd25519Signer(priv ed25519.PrivateKey) Ed25519Signer {
	return Ed25519Signer{
		priv: priv,
		pub:  Ed25519PubKey(priv.Public().(ed25519.PublicKey)),
	}
}

func (s Ed25519Signer) PubKey() PubKey {
	return s.pub
}

func (s Ed25519Signer) Sign(_ context.Context, input []byte) ([]byte, error) {
	return s.priv.Sign(nil, input, crypto.Hash(0))
}
