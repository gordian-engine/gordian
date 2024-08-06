package tmelink

// BlockDataArrival is shared with the engine's state machine,
// to indicate that a block's data has arrived.
//
// The mechanism by which block data arrives is driver-dependent;
// for example, it may be actively fetched upon observing a proposed block,
// or there may be a subscription interface where the the block data is passively received.
//
// Upon arrival of this data, the driver sends this value on a designated channel
// so that the consensus strategy may re-evaluate its proposed blocks,
// now being able to fully evaluate the proposed block with the data.
type BlockDataArrival struct {
	// The height and round of the arrived block data.
	// The height and round are compared against the state machine's current height and round,
	// and data arriving late or early are safely ignored.
	Height uint64
	Round  uint32

	// The DataID of the proposed block, whose data has arrived.
	ID string
}
