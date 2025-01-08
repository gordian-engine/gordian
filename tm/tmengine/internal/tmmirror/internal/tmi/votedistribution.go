package tmi

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// voteDistribution was an artifact of the no-longer-present tmconsensus.RoundState2 type.
// This type should be removed in favor of the VoteSummary field of tmconsensus.VersionedRoundView.
type voteDistribution struct {
	AvailableVotePower uint64

	VotePowerPresent uint64

	// Vote power by block hash.
	// Empty string key indicates a nil vote.
	BlockVotePower map[string]uint64
}

func newVoteDistribution(proofs map[string]gcrypto.CommonMessageSignatureProof, vals []tmconsensus.Validator) voteDistribution {
	d := voteDistribution{
		BlockVotePower: make(map[string]uint64, len(proofs)),
	}

	valsByKey := make(map[string]uint64, len(vals))
	for _, v := range vals {
		valsByKey[string(v.PubKey.PubKeyBytes())] = v.Power

		d.AvailableVotePower += v.Power
	}

	// TODO: derive hash of trustedVals, ensure each proof matches that hash.
	// Otherwise we risk reading an invalid proof and incorrectly calculating vote power.

	// TODO: ensure we don't double count a validator,
	// if one public key is present in multiple votes somehow.

	var bs bitset.BitSet
	for blockHash, proof := range proofs {
		proof.SignatureBitSet(&bs)
		for i, ok := bs.NextSet(0); ok && int(i) < len(vals); i, ok = bs.NextSet(i + 1) {
			pow := vals[int(i)].Power
			d.BlockVotePower[string(blockHash)] += pow
			d.VotePowerPresent += pow
		}
	}

	return d
}

// LogValue produces an slog.Value for cases where the VoteDistribution needs to be logged.
func (d voteDistribution) LogValue() slog.Value {
	voteBlocks := make([]string, 0, len(d.BlockVotePower))
	for hash, pow := range d.BlockVotePower {
		if hash == "" {
			voteBlocks = append(voteBlocks, fmt.Sprintf("nil => %d", pow))
		} else {
			voteBlocks = append(voteBlocks, fmt.Sprintf("%x => %d", hash, pow))
		}
	}
	sort.Strings(voteBlocks)
	return slog.GroupValue(
		slog.Uint64("vote_power_present", d.VotePowerPresent),
		slog.Uint64("available_vote_power", d.AvailableVotePower),
		slog.String("block_vote_power", strings.Join(voteBlocks, ", ")),
	)
}
