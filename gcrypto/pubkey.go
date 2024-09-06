package gcrypto

type PubKey interface {
	PubKeyBytes() []byte

	Equal(other PubKey) bool

	Verify(msg, sig []byte) bool
}
