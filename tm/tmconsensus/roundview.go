package tmconsensus

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
)

// RoundView is the engine's observed view of the state of a particular round.
//
// The RoundView may be on a later height and round, or with different validators,
// compared to the local state machine.
type RoundView struct {
	Height uint64
	Round  uint32

	ValidatorSet ValidatorSet

	PrevCommitProof CommitProof

	ProposedHeaders []ProposedHeader

	PrevoteProofs, PrecommitProofs map[string]gcrypto.CommonMessageSignatureProof

	VoteSummary VoteSummary
}

// Clone returns a RoundView, with values identical to v,
// and underlying slices and maps copied from v.
func (v *RoundView) Clone() RoundView {
	var prevoteClone map[string]gcrypto.CommonMessageSignatureProof
	if len(v.PrevoteProofs) > 0 {
		prevoteClone = make(map[string]gcrypto.CommonMessageSignatureProof, len(v.PrevoteProofs))
		for k, v := range v.PrevoteProofs {
			prevoteClone[k] = v.Clone()
		}
	}

	var precommitClone map[string]gcrypto.CommonMessageSignatureProof
	if len(v.PrecommitProofs) > 0 {
		precommitClone = make(map[string]gcrypto.CommonMessageSignatureProof, len(v.PrecommitProofs))
		for k, v := range v.PrecommitProofs {
			precommitClone[k] = v.Clone()
		}
	}

	return RoundView{
		Height: v.Height,
		Round:  v.Round,

		ValidatorSet: v.ValidatorSet,

		PrevCommitProof: v.PrevCommitProof.Clone(),

		ProposedHeaders: slices.Clone(v.ProposedHeaders),

		PrevoteProofs:   prevoteClone,
		PrecommitProofs: precommitClone,

		VoteSummary: v.VoteSummary.Clone(),
	}
}

// Reset zeros out all the fields of the RoundView,
// retaining any allocated capacity for its slices and maps.
// This is helpful for reusing RoundView values to avoid unnecessary memory allocations.
func (v *RoundView) Reset() {
	v.Height = 0

	v.PrevCommitProof.Round = 0
	v.PrevCommitProof.PubKeyHash = ""
	clear(v.PrevCommitProof.Proofs)

	v.ValidatorSet = ValidatorSet{}

	v.ResetForSameHeight()
	v.VoteSummary.Reset()
}

// ResetForSameHeight clears the round, proposed blocks, and vote information on v.
// It does not modify the height, validators, or validator hashes.
//
// This is intended to be used when it is known that a view is going to be reused in the same height,
// where it should be safe to keep the validator slice and validator hashes.
func (v *RoundView) ResetForSameHeight() {
	v.Round = 0

	clear(v.ProposedHeaders)
	v.ProposedHeaders = v.ProposedHeaders[:0]

	clear(v.PrevoteProofs)
	clear(v.PrecommitProofs)

	v.VoteSummary.ResetForSameHeight()
}

// LogValue converts v into an slog.Value.
// This provides a highly detailed log, so it is only appropriate for infrequent log events,
// such as responding to a watchdog termination signal.
func (v RoundView) LogValue() slog.Value {
	// This method is a value receiver because the slog package will not call the method
	// if it is a pointer receiver and we are not calling with a pointer.
	valAttrs := make([]slog.Attr, len(v.ValidatorSet.Validators))
	for i, val := range v.ValidatorSet.Validators {
		valAttrs[i] = slog.Attr{
			Key: fmt.Sprintf("%x", val.PubKey.PubKeyBytes()),
			Value: slog.GroupValue(
				slog.Int("index", i),
				slog.Uint64("power", val.Power),
			),
		}
	}

	pbHashes := make([]string, len(v.ProposedHeaders))
	for i, ph := range v.ProposedHeaders {
		pbHashes[i] = fmt.Sprintf("%x", ph)
	}

	var bs bitset.BitSet
	prevoteAttrs := make([]slog.Attr, 0, len(v.PrevoteProofs))
	for hash, proof := range v.PrevoteProofs {
		key := fmt.Sprintf("%x", hash)
		if key == "" {
			key = "<nil>"
		}
		proof.SignatureBitSet(&bs)
		prevoteAttrs = append(prevoteAttrs, slog.String(key, bs.String()))
	}
	sortSlogAttrsByKey(prevoteAttrs)

	precommitAttrs := make([]slog.Attr, 0, len(v.PrecommitProofs))
	for hash, proof := range v.PrecommitProofs {
		key := fmt.Sprintf("%x", hash)
		if key == "" {
			key = "<nil>"
		}
		proof.SignatureBitSet(&bs)
		precommitAttrs = append(precommitAttrs, slog.String(key, bs.String()))
	}
	sortSlogAttrsByKey(precommitAttrs)

	return slog.GroupValue(
		slog.Uint64("height", v.Height),
		slog.Uint64("round", uint64(v.Round)), // slog does not have a uint32 value.

		slog.Attr{Key: "validators", Value: slog.GroupValue(valAttrs...)},

		// Should this be using glog.Hex?
		slog.String("validator_pub_key_hash", fmt.Sprintf("%x", v.ValidatorSet.PubKeyHash)),
		slog.String("validator_vote_power_hash", fmt.Sprintf("%x", v.ValidatorSet.VotePowerHash)),

		slog.String("proposed_blocks", strings.Join(pbHashes, ", ")),

		slog.Attr{Key: "prevote_proofs", Value: slog.GroupValue(prevoteAttrs...)},
		slog.Attr{Key: "precommit_proofs", Value: slog.GroupValue(precommitAttrs...)},
	)
}

// VersionedRoundView is a superset of [RoundView]
// that contains version information,
// for use cases where a RoundView may be receiving live updates
// and a consumer may care to identify what has changed from one update to another.
//
// This type is used internally to the engine and exposed to the gossip strategy.
type VersionedRoundView struct {
	// Embedded round view for ease of access.
	RoundView

	// Overall version that gets incremented with each atomic change.
	// It is possible that the overall version is incremented once
	// while sub-versions are incremented multiple times.
	// It seems very unlikely that a view of a single height/round
	// would get anywhere close to 2^32 versions.
	Version uint32

	// There is no associated version for proposed headers;
	// the length of the ProposedHeaders slice is effectively the version.

	// The overall version of the particular vote.
	// It seems very unlikely that a view of a single height/round
	// would get anywhere close to 2^32 versions.
	PrevoteVersion, PrecommitVersion uint32

	// The version of the votes we have seen for particular blocks.
	// This is independent of the overall vote version in the previous field.
	// If we see a vote for block A, then the map will contain A=>1,
	// and if that was the first update, the overall vote version may be 2
	// (because initial state is version 1).
	//
	// Then if another update occurs where we see an additional vote for A
	// and a new vote for B, this map may contain A=>2 and B=>1,
	// whereas the overall version may have been incremented from 2 to 3.
	PrevoteBlockVersions, PrecommitBlockVersions map[string]uint32
}

// Clone returns a VersionedRoundView, with values identical to v,
// and underlying slices and maps copied from v.
func (v *VersionedRoundView) Clone() VersionedRoundView {
	return VersionedRoundView{
		RoundView: v.RoundView.Clone(),

		Version: v.Version,

		PrevoteVersion:   v.PrevoteVersion,
		PrecommitVersion: v.PrecommitVersion,

		PrevoteBlockVersions:   maps.Clone(v.PrevoteBlockVersions),
		PrecommitBlockVersions: maps.Clone(v.PrecommitBlockVersions),
	}
}

// Reset zeros out all the fields of the VersionedRoundView,
// retaining any allocated capacity for its slices and maps.
// This is helpful for reusing RoundView values to avoid unnecessary memory allocations.
func (v *VersionedRoundView) Reset() {
	v.RoundView.Reset()

	v.resetVersions()
}

// ResetForSameHeight resets the version information on the VersionedRoundView
// and calls v.RoundView.ResetForSameHeight.
//
// This is particularly useful when there is existing validator information
// on v.RoundView that should not be discarded.
func (v *VersionedRoundView) ResetForSameHeight() {
	v.RoundView.ResetForSameHeight()

	v.resetVersions()
}

// resetVersions clears only the version data on v.
// It is called from both the hard Reset method and the softer ResetForSameHeight method.
func (v *VersionedRoundView) resetVersions() {
	v.Version = 0
	v.PrevoteVersion = 0
	v.PrecommitVersion = 0

	clear(v.PrevoteBlockVersions)
	clear(v.PrecommitBlockVersions)
}

// LogValue converts v into an slog.Value.
// This provides a highly detailed log, so it is only appropriate for infrequent log events,
// such as responding to a watchdog termination signal.
func (v VersionedRoundView) LogValue() slog.Value {
	// This method is a value receiver because the slog package will not call the method
	// if it is a pointer receiver and we are not calling with a pointer.
	prevoteBlockVersionAttrs := make([]slog.Attr, 0, len(v.PrevoteBlockVersions))
	for hash, version := range v.PrevoteBlockVersions {
		key := fmt.Sprintf("%x", hash)
		if key == "" {
			key = "<nil>"
		}
		prevoteBlockVersionAttrs = append(prevoteBlockVersionAttrs, slog.Uint64(key, uint64(version)))
	}
	sortSlogAttrsByKey(prevoteBlockVersionAttrs)

	precommitBlockVersionAttrs := make([]slog.Attr, 0, len(v.PrecommitBlockVersions))
	for hash, version := range v.PrecommitBlockVersions {
		key := fmt.Sprintf("%x", hash)
		if key == "" {
			key = "<nil>"
		}
		precommitBlockVersionAttrs = append(precommitBlockVersionAttrs, slog.Uint64(key, uint64(version)))
	}
	sortSlogAttrsByKey(precommitBlockVersionAttrs)

	return slog.GroupValue(
		slog.Uint64("version", uint64(v.Version)),

		slog.Uint64("prevote_version", uint64(v.PrevoteVersion)),
		slog.Attr{Key: "prevote_block_versions", Value: slog.GroupValue(prevoteBlockVersionAttrs...)},

		slog.Uint64("precommit_version", uint64(v.PrecommitVersion)),
		slog.Attr{Key: "precommit_block_versions", Value: slog.GroupValue(precommitBlockVersionAttrs...)},

		slog.Attr{Key: "round_view", Value: v.RoundView.LogValue()},
	)
}

// sortSlogAttrsByKey does an in-place sort of attrs based on each attr's Key field.
// This is helpful to ensure deterministic formatting of logs with block hashes.
func sortSlogAttrsByKey(attrs []slog.Attr) {
	slices.SortFunc(attrs, func(a, b slog.Attr) int {
		return strings.Compare(a.Key, b.Key)
	})
}
