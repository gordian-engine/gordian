package gcmd

import (
	"crypto/ed25519"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/rollchains/gordian/gcrypto"
	"golang.org/x/crypto/blake2b"
)

func SignerFromInsecurePassphrase(prefix, insecurePassphrase string) (gcrypto.Ed25519Signer, error) {
	bh, err := blake2b.New(ed25519.SeedSize, nil)
	if err != nil {
		return gcrypto.Ed25519Signer{}, err
	}
	bh.Write([]byte(prefix + insecurePassphrase))
	seed := bh.Sum(nil)

	privKey := ed25519.NewKeyFromSeed(seed)

	return gcrypto.NewEd25519Signer(privKey), nil
}

func Libp2pKeyFromInsecurePassphrase(prefix, insecurePassphrase string) (libp2pcrypto.PrivKey, error) {
	bh, err := blake2b.New(ed25519.SeedSize, nil)
	if err != nil {
		return nil, err
	}
	bh.Write([]byte("gordian-echo:network|"))
	bh.Write([]byte(prefix + insecurePassphrase))
	seed := bh.Sum(nil)

	privKey := ed25519.NewKeyFromSeed(seed)

	priv, _, err := libp2pcrypto.KeyPairFromStdKey(&privKey)
	if err != nil {
		return nil, err
	}

	return priv, nil
}
