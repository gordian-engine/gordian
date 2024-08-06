// Package gchan contains helpers for common operations with channels.
// The helpers use consistent log formatting to save some boilerplate where used.
package gchan

import (
	"context"
	"log/slog"
	"time"
)

// SendC selects between ctx.Done and sending val to out.
// If ctx is canceled before the send to out completes,
// SendC logs the message "Context canceled while " + canceledDuring,
// and it reports false.
// Otherwise, val is successfully sent to out, and the function reports true.
func SendC[T any](ctx context.Context, log *slog.Logger, out chan<- T, val T, canceledDuring string) (sent bool) {
	select {
	case <-ctx.Done():
		log.Info("Context canceled while "+canceledDuring, "cause", context.Cause(ctx))
		return false
	case out <- val:
		return true
	}
}

// SendCLogBlocked behaves similar to [SendC] but logs if the send is blocked longer than tolerableBlockDuration.
// In that case, it logs the total blocked duration when the send eventually completes
// or when ctx is canceled.
//
// If the send completes successfully within tolerableBlockDuration,
// nothing is logged, matching SendC's behavior.
//
// This is useful for test helpers but generally should be avoided in production code.
func SendCLogBlocked[T any](
	ctx context.Context, log *slog.Logger,
	out chan<- T, val T,
	during string,
	tolerableBlockDuration time.Duration,
) (sent bool) {
	start := time.Now()

	if tolerableBlockDuration <= 0 {
		// Don't set up a timer if the caller wants to log immediately on blocked send.
		select {
		case <-ctx.Done():
			log.Info("Context canceled while "+during, "cause", context.Cause(ctx))
			return false
		case out <- val:
			return true
		default:
			log.Info("Blocked on initial send attempt while " + during)
		}
	} else {
		timer := time.NewTimer(tolerableBlockDuration)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			log.Info("Context canceled while "+during, "cause", context.Cause(ctx))
			return false
		case out <- val:
			return true
		case <-timer.C:
			log.Info("Blocked on send attempt while "+during, "dur", tolerableBlockDuration)
		}
	}

	// Now we block for the rest of the send.
	select {
	case <-ctx.Done():
		log.Info("Context canceled while "+during, "cause", context.Cause(ctx), "blocked_duration", time.Since(start))
		return false
	case out <- val:
		log.Info("Succesfully sent while "+during, "blocked_duration", time.Since(start))
		return true
	}
}

// RecvC selects between ctx.Done and receiving from in.
// If ctx is canceled before the receive from in completes,
// RecvC logs the message "Context canceled while " + canceledDuring,
// and it returns the zero value of T and reports false.
// Otherwise, the received value is returned and the function reports true.
func RecvC[T any](ctx context.Context, log *slog.Logger, in <-chan T, canceledDuring string) (val T, received bool) {
	select {
	case <-ctx.Done():
		log.Info("Context canceled while "+canceledDuring, "cause", context.Cause(ctx))
		return val, false
	case val := <-in:
		return val, true
	}
}

// RecvCLogBlocked behaves similar to [RecvC] but logs if the receive is blocked longer than tolerableBlockDuration.
// In that case, it logs the total blocked duration when the receive eventually completes
// or when ctx is canceled.
//
// If the receive completes successfully within tolerableBlockDuration,
// nothing is logged, matching RecvC's behavior.
//
// This is useful for test helpers but generally should be avoided in production code.
func RecvCLogBlocked[T any](
	ctx context.Context, log *slog.Logger,
	in <-chan T,
	during string,
	tolerableBlockDuration time.Duration,
) (val T, received bool) {
	start := time.Now()

	if tolerableBlockDuration <= 0 {
		// Don't set up a timer if the caller wants to log immediately on blocked receive.
		select {
		case <-ctx.Done():
			log.Info("Context canceled while "+during, "cause", context.Cause(ctx))
			return val, false
		case val := <-in:
			return val, true
		default:
			log.Info("Blocked on initial receive attempt while " + during)
		}
	} else {
		timer := time.NewTimer(tolerableBlockDuration)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			log.Info("Context canceled while "+during, "cause", context.Cause(ctx))
			return val, false
		case val := <-in:
			return val, true
		case <-timer.C:
			log.Info("Blocked on initial receive attempt while "+during, "dur", tolerableBlockDuration)
		}
	}

	// Now we block for the rest of the receive.
	select {
	case <-ctx.Done():
		log.Info("Context canceled while "+during, "cause", context.Cause(ctx), "blocked_duration", time.Since(start))
		return val, false
	case val := <-in:
		log.Info("Successfully received while "+during, "blocked_duration", time.Since(start))
		return val, true
	}
}

// ReqResp performs a blocking send of reqValue to reqChan,
// then waits to receive a value from respChan.
// If ctx is canceled during either operation,
// it returns the zero value of U and returns false.
//
// This is a useful shorthand for synchronous request-responses.
func ReqResp[T, U any](
	ctx context.Context, log *slog.Logger,
	reqChan chan<- T, reqValue T,
	respChan <-chan U,
	reqRespType string,
) (respVal U, ok bool) {
	if !SendC(ctx, log, reqChan, reqValue, "making "+reqRespType+" request") {
		return respVal, false
	}

	return RecvC(ctx, log, respChan, "receiving "+reqRespType+" response")
}
