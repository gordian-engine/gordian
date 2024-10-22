package tmconsensus

import "github.com/gordian-engine/gordian/gcrypto"

// HashScheme defines ways to determine various hashes in a consensus engine.
type HashScheme interface {
	// Block calculates and returns the block hash given a header,
	// without consulting or modifying existing Hash field on the header.
	Block(Header) ([]byte, error)

	// PubKeys calculates and returns the hash of the ordered set of public keys.
	PubKeys([]gcrypto.PubKey) ([]byte, error)

	// VotePowers calculates and returns the hash of the ordered set of voting power,
	// mapped 1:1 with the ordered set of public keys.
	VotePowers([]uint64) ([]byte, error)
}
