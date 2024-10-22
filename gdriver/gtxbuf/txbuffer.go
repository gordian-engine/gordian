package gtxbuf

import (
	"context"
	"errors"
	"log/slog"
	"runtime/trace"

	"github.com/gordian-engine/gordian/internal/gchan"
)

// Buffer is a validator-local transaction buffer.
// The type parameter S is chain state,
// and type paramter T is a transaction.
//
// Methods on Buffer are safe for concurrent use.
type Buffer[S, T any] struct {
	log *slog.Logger

	// Used for setting the initial state.
	// Cleared after first use.
	initCh chan S

	addTxRequests    chan addTxRequest[T]
	bufferedRequests chan bufferedRequest[T]
	rebaseRequests   chan rebaseRequest[S, T]

	done chan struct{}
}

type addTxRequest[T any] struct {
	Tx   T
	Resp chan error
}

type bufferedRequest[T any] struct {
	Dst  []T
	Resp chan []T
}

type rebaseRequest[S, T any] struct {
	BaseState  S
	AppliedTxs []T
	Resp       chan rebaseResponse[T]
}

type rebaseResponse[T any] struct {
	Invalidated []T
	Err         error
}

// New returns a new Buffer for the given state and transaction types.
//
// The addTxFunc must return a new copy of the given state,
// representing the updated state as a result of applying the transaction.
// During a call to AddTx, errors are returned directly to the caller.
// During rebase, an error returned by addTxFunc is assumed to be fatal,
// unless it is wrapped in [TxInvalidError].
//
// The txDeleterFunc argument is used to produce a function
// passed to [slices.Delete],
// so that returned function must report true for transactions
// that must be removed from the buffer.
// In most cases, that will involve creating a map of reject values,
// and returning a function closing over the map,
// reporting presence of the given transaction in that map.
func New[S, T any](
	ctx context.Context,
	log *slog.Logger,
	addTxFunc func(ctx context.Context, state S, tx T) (S, error),
	txDeleterFunc func(ctx context.Context, reject []T) func(tx T) bool,
) *Buffer[S, T] {
	b := &Buffer[S, T]{
		log: log,

		initCh: make(chan S),

		addTxRequests:    make(chan addTxRequest[T]),
		bufferedRequests: make(chan bufferedRequest[T]),
		rebaseRequests:   make(chan rebaseRequest[S, T]),

		done: make(chan struct{}),
	}

	go b.kernel(ctx, addTxFunc, txDeleterFunc)

	return b
}

func (b *Buffer[S, T]) kernel(
	ctx context.Context,
	addTxFunc func(context.Context, S, T) (S, error),
	txDeleterFunc func(context.Context, []T) func(T) bool,
) {
	defer close(b.done)

	ctx, task := trace.NewTask(ctx, "gtxbuf.Buffer.kernel")
	defer task.End()

	// Can't do anything until initial state is set.
	baseState, ok := gchan.RecvC(
		ctx, b.log,
		b.initCh,
		"waiting for initial state",
	)
	if !ok {
		return
	}

	// The initialization channel is not usable any more.
	// We could just close it to prevent further sends,
	// but setting it to nil is one less object for GC to scan,
	// and it allows us to give a meaningful panic
	// if SetInitialState is called a second time.
	b.initCh = nil

	w := workingState[S, T]{
		BaseState: baseState,
		addTx:     addTxFunc,
		txDeleter: txDeleterFunc,
	}

	// Now the actual main loop.
	for {
		select {
		case <-ctx.Done():
			b.log.Info("Shutting down due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-b.addTxRequests:
			b.handleAddTx(ctx, &w, req)

		case req := <-b.bufferedRequests:
			b.handleBuffered(ctx, &w, req)

		case req := <-b.rebaseRequests:
			b.handleRebase(ctx, &w, req)
		}
	}
}

// Wait blocks until all background work for b is finished.
// Initiate a clean shutdown by closing the context passed to [New].
func (b *Buffer[S, T]) Wait() {
	<-b.done
}

// Initialize must be the first method called on a new buffer,
// to initialize the state.
// If called a second time, Initialize will panic.
//
// The initial state is not part of [New] because it is assumed
// that the buffer is needed to propagate through the driver
// before the genesis state may be ready.
func (b *Buffer[S, T]) Initialize(ctx context.Context, initialState S) (ok bool) {
	if b.initCh == nil {
		panic(errors.New("BUG: (*gtxbuf.Buffer).Initialize called twice"))
	}

	return gchan.SendC(
		ctx, b.log,
		b.initCh, initialState,
		"sending initial state to buffer",
	)
}

func (b *Buffer[S, T]) handleAddTx(ctx context.Context, w *workingState[S, T], req addTxRequest[T]) {
	// The trace region may be meaningful depending on how long it takes
	// to attempt to add the transaction.
	defer trace.StartRegion(ctx, "handleAddTx").End()

	// Response channel is one-buffered, so don't select here.
	req.Resp <- w.CheckAddTx(ctx, req.Tx)
}

// AddTx attempts to modify b's state by temporarily executing the transaction t,
// using the addTxFunc passed to [New].
// Any error returned by addTxFunc is returned directly.
func (b *Buffer[S, T]) AddTx(ctx context.Context, tx T) error {
	req := addTxRequest[T]{
		Tx:   tx,
		Resp: make(chan error, 1),
	}

	err, ok := gchan.ReqResp(
		ctx, b.log,
		b.addTxRequests, req,
		req.Resp,
		"making AddTx request",
	)

	if !ok {
		return context.Cause(ctx)
	}

	return err
}

func (b *Buffer[S, T]) handleBuffered(ctx context.Context, w *workingState[S, T], req bufferedRequest[T]) {
	defer trace.StartRegion(ctx, "handleBuffered").End()

	out := w.Buffered(req.Dst)
	req.Resp <- out
}

// Buffered returns a copy of the pending transactions in b.
// The copied values are appended to dst, which may be nil,
// and the result is returned.
func (b *Buffer[S, T]) Buffered(ctx context.Context, dst []T) []T {
	req := bufferedRequest[T]{
		Dst:  dst,
		Resp: make(chan []T, 1),
	}

	out, _ := gchan.ReqResp(
		ctx, b.log,
		b.bufferedRequests, req,
		req.Resp,
		"requesting buffered transactions",
	)

	return out
}

func (b *Buffer[S, T]) handleRebase(ctx context.Context, w *workingState[S, T], req rebaseRequest[S, T]) {
	defer trace.StartRegion(ctx, "handleRebase").End()

	req.Resp <- w.Rebase(ctx, req.BaseState, req.AppliedTxs)
}

// Rebase changes b's root state to newBase,
// then calls the txDeleterFunc to remove pending transactions
// that are present in the applied slice.
// It is important that the addTxFunc uses [TxInvalidError] to wrap errors
// for transactions that are no longer valid with the new state;
// errors not wrapped in [TxInvalidError] are assumed to be fatal.
func (b *Buffer[S, T]) Rebase(
	ctx context.Context, newBase S, applied []T,
) (invalidated []T, err error) {
	req := rebaseRequest[S, T]{
		BaseState:  newBase,
		AppliedTxs: applied,
		Resp:       make(chan rebaseResponse[T], 1),
	}

	resp, ok := gchan.ReqResp(
		ctx, b.log,
		b.rebaseRequests, req,
		req.Resp,
		"requesting rebase",
	)
	if !ok {
		return nil, context.Cause(ctx)
	}

	return resp.Invalidated, resp.Err
}
