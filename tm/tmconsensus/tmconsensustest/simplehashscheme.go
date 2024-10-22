package tmconsensustest

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"golang.org/x/crypto/blake2b"
)

type SimpleHashScheme struct{}

func (SimpleHashScheme) Block(h tmconsensus.Header) ([]byte, error) {
	hasher, err := blake2b.New(32, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create new blake2b hasher: %w", err)
	}

	var buf bytes.Buffer

	// Serialize the previous commit proof.
	// First iterate over the voted blocks in order.
	prevCommitBlocks := make([]string, 0, len(h.PrevCommitProof.Proofs))
	for bh := range h.PrevCommitProof.Proofs {
		var blockKey string
		if bh == "" {
			blockKey = "<nil>"
		} else {
			blockKey = fmt.Sprintf("%x", bh)
		}
		prevCommitBlocks = append(prevCommitBlocks, blockKey)
	}
	sort.Strings(prevCommitBlocks)

	for i, blockHash := range prevCommitBlocks {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(blockHash)
		buf.WriteString(" => (")
		sigs := h.PrevCommitProof.Proofs[blockHash]

		sigStrings := make([]string, len(sigs))
		for j, sig := range sigs {
			sigStrings[j] = fmt.Sprintf("%x:%x", sig.KeyID, sig.Sig)
		}
		sort.Strings(sigStrings)

		for j, s := range sigStrings {
			if j > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(s)
		}
		buf.WriteByte(')')
	}
	prevCommitSignatures := buf.String()

	fmt.Fprintf(hasher, `BLOCK
PrevBlockHash: %x
Height: %d
PrevCommitProof:
  Round: %d
  PubKeyHash: %x
  Signatures: %s
ValidatorSet: %x.%x
NextValidatorSet: %x.%x
DataID: %x
PrevAppStateHash: %x
`,
		h.PrevBlockHash,
		h.Height,
		h.PrevCommitProof.Round,
		h.PrevCommitProof.PubKeyHash,
		prevCommitSignatures,
		h.ValidatorSet.PubKeyHash, h.ValidatorSet.VotePowerHash,
		h.NextValidatorSet.PubKeyHash, h.NextValidatorSet.VotePowerHash,
		h.DataID,
		h.PrevAppStateHash,
	)

	if h.Annotations.User != nil {
		fmt.Fprintf(hasher, "UserAnnotation: %x\n", h.Annotations.User)
	}
	if h.Annotations.Driver != nil {
		fmt.Fprintf(hasher, "DriverAnnotation: %x\n", h.Annotations.Driver)
	}

	return hasher.Sum(nil), nil
}

func (SimpleHashScheme) PubKeys(keys []gcrypto.PubKey) ([]byte, error) {
	if len(keys) == 0 {
		panic(errors.New("BUG: HashScheme.PubKeys should never be called with zero keys"))
	}

	hasher, err := blake2b.New(32, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create new blake2b hasher: %w", err)
	}

	var buf bytes.Buffer
	first := fmt.Appendf(nil, "%x", keys[0].PubKeyBytes())
	buf.Grow(
		len(keys)*len(first) + // Same length for each public key
			len(first) - 1, // And one newline byte between each key.
	)
	buf.Write(first)
	for _, k := range keys[1:] {
		buf.WriteByte('\n')
		fmt.Fprintf(&buf, "%x", k.PubKeyBytes())
	}

	_, _ = hasher.Write(buf.Bytes())
	return hasher.Sum(nil), nil
}

func (SimpleHashScheme) VotePowers(pows []uint64) ([]byte, error) {
	if len(pows) == 0 {
		panic(errors.New("BUG: HashScheme.VotePowers should never be called with zero powers"))
	}

	hasher, err := blake2b.New(32, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create new blake2b hasher: %w", err)
	}

	var buf bytes.Buffer
	for i, pow := range pows {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "%d", pow)
	}

	_, _ = hasher.Write(buf.Bytes())
	return hasher.Sum(nil), nil
}
