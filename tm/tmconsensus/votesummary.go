package tmconsensus

import (
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
)

// VoteSummary summarizes the known votes in a round.
type VoteSummary struct {
	// The total voting power of the validators.
	AvailablePower uint64

	// The cumulative voting power present for prevotes or precommits.
	TotalPrevotePower, TotalPrecommitPower uint64

	// The voting power by block hash for prevotes or precommits.
	PrevoteBlockPower, PrecommitBlockPower map[string]uint64

	// Which prevote or precommit currently has the most votes.
	// If nothing has any votes, or if nil has the most votes, this is the empty string.
	// If there is a tie in voting power,
	// the value will be the lexicographically earlier hash, for consistency purposes.
	MostVotedPrevoteHash, MostVotedPrecommitHash string
}

func NewVoteSummary() VoteSummary {
	return VoteSummary{
		PrevoteBlockPower:   make(map[string]uint64),
		PrecommitBlockPower: make(map[string]uint64),
	}
}

func (vs VoteSummary) Clone() VoteSummary {
	return VoteSummary{
		AvailablePower: vs.AvailablePower,

		TotalPrevotePower:   vs.TotalPrevotePower,
		TotalPrecommitPower: vs.TotalPrecommitPower,

		PrevoteBlockPower:   maps.Clone(vs.PrevoteBlockPower),
		PrecommitBlockPower: maps.Clone(vs.PrecommitBlockPower),

		MostVotedPrevoteHash:   vs.MostVotedPrevoteHash,
		MostVotedPrecommitHash: vs.MostVotedPrecommitHash,
	}
}

func (vs *VoteSummary) SetAvailablePower(vals []Validator) {
	vs.AvailablePower = 0
	for _, v := range vals {
		vs.AvailablePower += v.Power
	}
}

func (vs *VoteSummary) SetVotePowers(vals []Validator, prevotes, precommits map[string]gcrypto.CommonMessageSignatureProof) {
	vs.SetPrevotePowers(vals, prevotes)
	vs.SetPrecommitPowers(vals, precommits)
}

func (vs *VoteSummary) SetPrevotePowers(vals []Validator, prevotes map[string]gcrypto.CommonMessageSignatureProof) {
	vs.TotalPrevotePower = 0

	clear(vs.PrevoteBlockPower)

	var maxHash string
	var maxPow uint64
	var bs bitset.BitSet
	for blockHash, proof := range prevotes {
		proof.SignatureBitSet(&bs)
		var blockPow uint64
		for i, ok := bs.NextSet(0); ok && int(i) < len(vals); i, ok = bs.NextSet(i + 1) {
			valPow := vals[int(i)].Power
			vs.TotalPrevotePower += valPow
			blockPow += valPow
		}

		vs.PrevoteBlockPower[string(blockHash)] = blockPow
		if blockPow == maxPow {
			maxHash = min(maxHash, blockHash)
		} else if blockPow > maxPow {
			maxPow = blockPow
			maxHash = blockHash
		}
	}

	vs.MostVotedPrevoteHash = maxHash
}

func (vs *VoteSummary) SetPrecommitPowers(vals []Validator, precommits map[string]gcrypto.CommonMessageSignatureProof) {
	vs.TotalPrecommitPower = 0

	clear(vs.PrecommitBlockPower)

	var maxHash string
	var maxPow uint64
	var bs bitset.BitSet
	for blockHash, proof := range precommits {
		proof.SignatureBitSet(&bs)
		var blockPow uint64
		for i, ok := bs.NextSet(0); ok && int(i) < len(vals); i, ok = bs.NextSet(i + 1) {
			valPow := vals[int(i)].Power
			vs.TotalPrecommitPower += valPow
			blockPow += valPow
		}

		vs.PrecommitBlockPower[string(blockHash)] = blockPow
		if blockPow == maxPow {
			maxHash = min(maxHash, blockHash)
		} else if blockPow > maxPow {
			maxPow = blockPow
			maxHash = blockHash
		}
	}

	vs.MostVotedPrecommitHash = maxHash
}

func (vs *VoteSummary) Reset() {
	vs.AvailablePower = 0
	vs.ResetForSameHeight()
}

func (vs *VoteSummary) ResetForSameHeight() {
	vs.TotalPrevotePower = 0
	vs.TotalPrecommitPower = 0
	clear(vs.PrevoteBlockPower)
	clear(vs.PrecommitBlockPower)
	vs.MostVotedPrevoteHash = ""
	vs.MostVotedPrecommitHash = ""
}

func (vs VoteSummary) LogValue() slog.Value {
	prevoteBlocks := make([]string, 0, len(vs.PrevoteBlockPower))
	for hash, pow := range vs.PrevoteBlockPower {
		if hash == "" {
			if pow > 0 {
				// The nil block is always present, but we don't want to log it
				// if there are no votes.
				prevoteBlocks = append(prevoteBlocks, fmt.Sprintf("nil => %d", pow))
			}
		} else {
			prevoteBlocks = append(prevoteBlocks, fmt.Sprintf("%x => %d", hash, pow))
		}
	}
	sort.Strings(prevoteBlocks)

	precommitBlocks := make([]string, 0, len(vs.PrecommitBlockPower))
	for hash, pow := range vs.PrecommitBlockPower {
		if hash == "" {
			if pow > 0 {
				precommitBlocks = append(precommitBlocks, fmt.Sprintf("nil => %d", pow))
			}
		} else {
			precommitBlocks = append(precommitBlocks, fmt.Sprintf("%x => %d", hash, pow))
		}
	}
	sort.Strings(precommitBlocks)

	return slog.GroupValue(
		slog.Uint64("available_power", vs.AvailablePower),
		slog.Uint64("prevote_power", vs.TotalPrevotePower),
		slog.String("prevote_block_power", strings.Join(prevoteBlocks, ", ")),
		slog.Uint64("precommit_power", vs.TotalPrecommitPower),
		slog.String("precommit_block_power", strings.Join(precommitBlocks, ", ")),
	)
}
