package types

// Proposer represents a validator that could propose in upcoming rounds
type Proposer struct {
	NodeID   string
	Height   int64
	Round    int32
	Priority int // Lower number = higher priority
}

// RoundUpdate captures consensus state changes
type RoundUpdate struct {
	Height           int64
	Round           int32
	IsLeader        bool
	CommittedTxs    [][]byte // Transactions that were committed in last block
	NextProposers   []Proposer
}