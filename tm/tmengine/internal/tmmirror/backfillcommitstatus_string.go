// Code generated by "stringer -type backfillCommitStatus -trimprefix=backfillCommit"; DO NOT EDIT.

package tmmirror

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[backfillCommitInvalid-0]
	_ = x[backfillCommitAccepted-1]
	_ = x[backfillCommitPubKeyHashMismatch-2]
	_ = x[backfillCommitRejected-3]
}

const _backfillCommitStatus_name = "InvalidAcceptedPubKeyHashMismatchRejected"

var _backfillCommitStatus_index = [...]uint8{0, 7, 15, 33, 41}

func (i backfillCommitStatus) String() string {
	if i >= backfillCommitStatus(len(_backfillCommitStatus_index)-1) {
		return "backfillCommitStatus(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _backfillCommitStatus_name[_backfillCommitStatus_index[i]:_backfillCommitStatus_index[i+1]]
}
