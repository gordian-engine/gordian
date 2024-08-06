package gcsecp256k1

import (
	"context"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/rollchains/gordian/gcrypto"
)

var _ gcrypto.Signer = Signer{}
var _ gcrypto.PubKey = (*PubKey)(nil)

type Signer struct {
	priv secp256k1.PrivKey
	pub  *PubKey
}

func NewSigner(priv secp256k1.PrivKey) Signer {
	pubKey := priv.PubKey().(*secp256k1.PubKey)
	return Signer{
		priv: priv,
		pub:  (*PubKey)(pubKey),
	}
}

func (s Signer) PubKey() gcrypto.PubKey {
	return s.pub
}

func (s Signer) Sign(_ context.Context, input []byte) (signature []byte, err error) {
	return s.priv.Sign(input)
}

type PubKey secp256k1.PubKey

func (k *PubKey) PubKeyBytes() []byte {
	return (*secp256k1.PubKey)(k).Bytes()
}

func (k *PubKey) Equal(other gcrypto.PubKey) bool {
	o, ok := other.(*PubKey)

	return ok && (*secp256k1.PubKey)(k).Equals((*secp256k1.PubKey)(o))
}

func (k *PubKey) Verify(msg, sig []byte) bool {
	return (*secp256k1.PubKey)(k).VerifySignature(msg, sig)
}
