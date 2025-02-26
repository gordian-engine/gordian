// Code generated by "stringer -type HandleProposedHeaderResult -trimprefix=HandleProposedHeader ."; DO NOT EDIT.

package tmconsensus

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[HandleProposedHeaderAccepted-1]
	_ = x[HandleProposedHeaderAlreadyStored-2]
	_ = x[HandleProposedHeaderSignerUnrecognized-3]
	_ = x[HandleProposedHeaderBadBlockHash-4]
	_ = x[HandleProposedHeaderBadSignature-5]
	_ = x[HandleProposedHeaderMissingProposerPubKey-6]
	_ = x[HandleProposedHeaderBadPrevCommitProofPubKeyHash-7]
	_ = x[HandleProposedHeaderBadPrevCommitProofSignature-8]
	_ = x[HandleProposedHeaderBadPrevCommitProofDoubleSigned-9]
	_ = x[HandleProposedHeaderBadPrevCommitVoteCount-10]
	_ = x[HandleProposedHeaderRoundTooOld-11]
	_ = x[HandleProposedHeaderRoundTooFarInFuture-12]
	_ = x[HandleProposedHeaderInternalError-13]
}

const _HandleProposedHeaderResult_name = "AcceptedAlreadyStoredSignerUnrecognizedBadBlockHashBadSignatureMissingProposerPubKeyBadPrevCommitProofPubKeyHashBadPrevCommitProofSignatureBadPrevCommitProofDoubleSignedBadPrevCommitVoteCountRoundTooOldRoundTooFarInFutureInternalError"

var _HandleProposedHeaderResult_index = [...]uint8{0, 8, 21, 39, 51, 63, 84, 112, 139, 169, 191, 202, 221, 234}

func (i HandleProposedHeaderResult) String() string {
	i -= 1
	if i >= HandleProposedHeaderResult(len(_HandleProposedHeaderResult_index)-1) {
		return "HandleProposedHeaderResult(" + strconv.FormatInt(int64(i+1), 10) + ")"
	}
	return _HandleProposedHeaderResult_name[_HandleProposedHeaderResult_index[i]:_HandleProposedHeaderResult_index[i+1]]
}
