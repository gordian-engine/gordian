# Concurrency Patterns in Gordian

This is an outline of the principles behind the concurrency design patterns in Gordian and how they are implemented. It is meant as a guide to help developers understand the design decisions behind the codebase and to help them write their own concurrent code.

### 1. Mutable *and stateful* changes are localized to one goroutine

> **General rule:** no mutexes

> **Exception:** plain old get-set objects don’t need a coordinating goroutine. 

```go
type ConcurrentBidirectionalMap struct {
  mu sync.Mutex
  si map[string]int
  is map[int]string
}

func (m *C...Map) Set(s string, i int) {
  m.mu.Lock()
  defer m.mu.Unlock()
  m.si[s] = i
  m.is[i] = s
}

// func GetI, func GetS
```

In types like the above there is no statefulness. The mutex is held for an instant while modifying a map and there is no further concurrent interaction with any other types.

This pattern avoids multiple classes of problems associated with complicated uses of mutexes

- Difficulty in tracking all call sites which may lock a mutex to discover where a deadlock may occur
- No need for multiple mutexes to track different granularity of locks
- No need to decide between plain `Mutex` and `RWMutex`

There are a couple of tradeoffs with this “control loop” or “single writer” style pattern. I do not call them downsides because they are straightforward to manage.

-  Panics are typically unrecoverable because they happen in a goroutine other than the caller; if you have a panic they are a bit more intensive to find the root cause when it isn’t obvious
-  Returning values from the control loop goroutine means that you usually have to have a request-response pair of types; the response returns a _**copy**_ of the writable data that the main goroutine owns. If used improperly, this can lead to a lot of garbage creation, but there are multiple mitigation strategies to avoid that.
-  Clean execution of this pattern involves widespread use of `select` and generally, every send and every receive is going to be in a `select` at least also watching a root `context.Context` for cancellation. So naturally there are going to be some helper functions for the common case of sending to or receiving from one channel while watching that root context. (TODO: add links to or examples of these helper functions)

### 2. The application has a determinate number of goroutines

We create a fixed number of goroutines within every type.

Generally, there is one “thread” of work that happens in a single goroutine, or there is massively parallelizable background work that happens in a collection of `N > 1` goroutines, and typically `N == runtime.GOMAXPROCS(0)` (i.e. one goroutine per available CPU core unless otherwise configured).

Short-lived goroutines are and should be exceedingly rare.

The goroutines’ lifecycles are coupled with a root `context.Context` and they stop when that context is cancelled. There is almost never a need for a separate stop/quit signal. In that event, use a new `context.WithCancel(rootCtx)`.

While the go runtime has of course made optimizations for running many short-lived goroutines, the problem with creating a new goroutine on every particular external event (say an incoming RPC request) is that we cannot provide backpressure to the client via creation of the goroutine.

### 3. Understand and correctly use channel sizing

Unbuffered channels (`ch := make(chan X)` or less commonly `make(chan X, 0)`) are used when the sender needs to know that the receiver has received the sent value.

Buffered channels (`ch := make(chan X, n)` for `n > 0`) are used to enqueue work that will be completed at some point in the future, or are used  to ensure that at least n sends to the channel can occur without blocking (and therefore do not need wrapped in a `select`).

In Gordian we use unbuffered channels in two primary cases:

- boring “signals” with closed channels (will be discussed further in next major point)
- batching collections of updates, where we need to be certain that the receiver has the data we sent.

And by corollary, the buffered channels are closer to a fire and forget pattern. When we have a request-response pair, we generally have the pattern

```go
type FooRequest struct {
  Input int
  Resp FooResponse
}

type FooResponse struct {
  Bar int
  Baz string
}

// Buffered so that receiver can send without blocking.
resp := make(chan FooResponse, 1)

// (These two sends would select against context normally but abbreviated here)
outgoingRequests <- FooRequest{
  Input: input,
  Resp: resp,
}

got := <-resp
doSomething(got.Bar, got.Baz)
```

### 4. Understand and correctly use general properties of channels

The following pattern allows us to have one large `select` statement where we have conditional assignment of channels to avoid certain cases:

- Sends to a `nil` channel block forever, so they will never be chosen in a `select` statement
- Receives from a nil channel block forever, so they will never be chosen in a select statement either

```go
for {
  var toChildCh := self.childOutputCh
  if self.childOutputIsUpToDate() {
    toChildCh = nil
  }

  select {
    case <-ctx.Done():
      return
    case req := <-self.incomingWorkCh:
      self.doWork(ctx, req)
    case toChildCh <- self.toChildVal:
      // Sometimes toChildCh is nil, and if so that case will never be chosen.
      // This allows us to only have one select statement instead of many, mostly duplicated selects.
      self.markChildOutputUpToDate()
  }
}
```

- Sending a value on a channel will only be received by one ready reader at random
- Closing a channel is immediately visible to all ready readers

We rely on B1 to distribute background work evenly.

We rely on B2 to send general signals to many receivers.

You may see examples in the wild where a writer sends many values into one channel and then closes the channel to indicate that there are no more values. IME that pattern is fine for a short-lived program (e.g., a handwritten utility to do a single batch task and quit, like perhaps checksumming every file in a directory tree) but for a long-lived service, because we are not starting and stopping goroutines repeatedly, the “quit processing this stream of work” signal is typically not helpful.
Where we do use the close channel pattern is most often to indicate that a goroutine has completed.

```go
type Worker struct {
  // some other relevant fields...
  done chan struct{}
}

func (w *Worker) doWork() {
  defer close(w.done)

  // do the work...
}

func (w *Worker) Wait() {
  <-w.done
}
```

We use the `Wait` pattern extensively to ensure that, in tests especially, all our workers finish when their root context is cancelled. This matters in production code to be absolutely certain we can cleanly shut down on interrupt. You do not want incomplete, hanging work that requires `^c^c^c^c^c^z kill -9 $(pidof myprog)`

### 5. Batch channel values together where it makes sense

It’s a small optimization, but there is overhead in every `select` and in every channel that the runtime maintains, and there is overhead in switching active goroutines, so if `Parent` sends multiple values to `Child` and those values are somehow related, prefer `toChildCh <- oneValWithMultipleOptionalFields` over `fooToChildCh <- foo; barToChildCh <- bar; bazToChildCh <- baz`
