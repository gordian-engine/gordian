package network

// TransactionBatch represents a batch of transactions to send to a proposer
type TransactionBatch struct {
	Transactions [][]byte
	NodeID       string // Destination node
	Height       int64  // Target height
	Round        int32  // Target round
}

// Stats tracks network client statistics
type Stats struct {
	BatchesSent uint64
	TxSent      uint64
	SendErrors  uint64
	ActiveSends uint32 // Currently active send operations
}