package gcrypto

type PubKey interface {
	Address() []byte

	PubKeyBytes() []byte

	Equal(other PubKey) bool

	Verify(msg, sig []byte) bool
}
