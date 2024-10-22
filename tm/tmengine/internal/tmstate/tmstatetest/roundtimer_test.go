package tmstatetest_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate/tmstatetest"
	"github.com/stretchr/testify/require"
)

func TestMockRoundTimer_sync(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		notifyFn func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) <-chan struct{}
		timerFn  func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) (<-chan struct{}, func())
	}{
		{
			name: "proposal",
			notifyFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) <-chan struct{} {
				return rt.ProposalStartNotification(h, r)
			},
			timerFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) (<-chan struct{}, func()) {
				return rt.ProposalTimer(ctx, h, r)
			},
		},
		{
			name: "prevote delay",
			notifyFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) <-chan struct{} {
				return rt.PrevoteDelayStartNotification(h, r)
			},
			timerFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) (<-chan struct{}, func()) {
				return rt.PrevoteDelayTimer(ctx, h, r)
			},
		},
		{
			name: "precommit delay",
			notifyFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) <-chan struct{} {
				return rt.PrecommitDelayStartNotification(h, r)
			},
			timerFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) (<-chan struct{}, func()) {
				return rt.PrecommitDelayTimer(ctx, h, r)
			},
		},
		{
			name: "commit wait",
			notifyFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) <-chan struct{} {
				return rt.CommitWaitStartNotification(h, r)
			},
			timerFn: func(rt *tmstatetest.MockRoundTimer, h uint64, r uint32) (<-chan struct{}, func()) {
				return rt.CommitWaitTimer(ctx, h, r)
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var rt tmstatetest.MockRoundTimer

			ch := tc.notifyFn(&rt, 1, 0)
			require.NotNil(t, ch)
			gtest.NotSending(t, ch)

			elapsed, cancel := tc.timerFn(&rt, 1, 0)
			defer cancel()
			require.NotNil(t, elapsed)

			select {
			case <-ch:
				// Okay.
			default:
				t.Fatalf("%s start notification should have been closed immediately upon starting the timer", tc.name)
			}
		})
	}
}
