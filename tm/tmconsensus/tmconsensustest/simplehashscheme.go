package tmconsensustest

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"golang.org/x/crypto/blake2b"
)

type SimpleHashScheme struct{}

func (SimpleHashScheme) Block(b tmconsensus.Block) ([]byte, error) {
	hasher, err := blake2b.New(32, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create new blake2b hasher: %w", err)
	}

	// Current validators' ID and power.
	var buf bytes.Buffer
	for i, v := range b.Validators {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "%x/%d", v.PubKey.PubKeyBytes(), v.Power)
	}
	valData := buf.String()

	// Next validators' ID and power.
	buf.Reset()
	for i, v := range b.NextValidators {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "%x/%d", v.PubKey.PubKeyBytes(), v.Power)
	}
	nextValData := buf.String()

	// Serialize the previous commit proof.
	// First iterate over the voted blocks in order.
	buf.Reset()
	prevCommitBlocks := make([]string, 0, len(b.PrevCommitProof.Proofs))
	for h := range b.PrevCommitProof.Proofs {
		var blockKey string
		if h == "" {
			blockKey = "<nil>"
		} else {
			blockKey = fmt.Sprintf("%x", h)
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
		sigs := b.PrevCommitProof.Proofs[blockHash]

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
Validators: %s
NextValidators: %s
DataID: %x
PrevAppStateHash: %x
`,
		b.PrevBlockHash,
		b.Height,
		b.PrevCommitProof.Round,
		b.PrevCommitProof.PubKeyHash,
		prevCommitSignatures,
		valData,
		nextValData,
		b.DataID,
		b.PrevAppStateHash,
	)

	if b.Annotations.User != nil {
		fmt.Fprintf(hasher, "UserAnnotation: %x\n", b.Annotations.User)
	}
	if b.Annotations.Driver != nil {
		fmt.Fprintf(hasher, "DriverAnnotation: %x\n", b.Annotations.Driver)
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
