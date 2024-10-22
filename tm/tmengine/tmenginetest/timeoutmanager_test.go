package tmenginetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/tm/internal/tmtimeout"
	tmenginetest "github.com/gordian-engine/gordian/tm/tmengine/tmenginetest"
	"github.com/stretchr/testify/require"
)

func TestTimeoutManager(t *testing.T) {
	for _, tc := range []struct {
		name        string
		Create      func(context.Context, *tmenginetest.TimeoutManager, uint64, uint32) (context.Context, context.CancelFunc)
		Outstanding func(*tmenginetest.TimeoutManager, uint64, uint32) int
		Trigger     func(*tmenginetest.TimeoutManager, uint64, uint32) int
		WantErr     error
	}{
		{
			name: "proposal timeout",
			Create: func(ctx context.Context, tm *tmenginetest.TimeoutManager, h uint64, r uint32) (context.Context, context.CancelFunc) {
				return tm.WithProposalTimeout(ctx, h, r)
			},
			Outstanding: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.OutstandingProposalTimeouts(h, r)
			},
			Trigger: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.TriggerProposalTimeout(h, r)
			},
			WantErr: tmtimeout.ErrProposalTimedOut,
		},

		{
			name: "prevote delay timeout",
			Create: func(ctx context.Context, tm *tmenginetest.TimeoutManager, h uint64, r uint32) (context.Context, context.CancelFunc) {
				return tm.WithPrevoteDelayTimeout(ctx, h, r)
			},
			Outstanding: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.OutstandingPrevoteDelayTimeouts(h, r)
			},
			Trigger: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.TriggerPrevoteDelayTimeout(h, r)
			},
			WantErr: tmtimeout.ErrPrevoteDelayTimedOut,
		},

		{
			name: "precommit delay timeout",
			Create: func(ctx context.Context, tm *tmenginetest.TimeoutManager, h uint64, r uint32) (context.Context, context.CancelFunc) {
				return tm.WithPrecommitDelayTimeout(ctx, h, r)
			},
			Outstanding: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.OutstandingPrecommitDelayTimeouts(h, r)
			},
			Trigger: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.TriggerPrecommitDelayTimeout(h, r)
			},
			WantErr: tmtimeout.ErrPrecommitDelayTimedOut,
		},

		{
			name: "commit wait timeout",
			Create: func(ctx context.Context, tm *tmenginetest.TimeoutManager, h uint64, r uint32) (context.Context, context.CancelFunc) {
				return tm.WithCommitWaitTimeout(ctx, h, r)
			},
			Outstanding: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.OutstandingCommitWaitTimeouts(h, r)
			},
			Trigger: func(tm *tmenginetest.TimeoutManager, h uint64, r uint32) int {
				return tm.TriggerCommitWaitTimeout(h, r)
			},
			WantErr: tmtimeout.ErrCommitWaitTimedOut,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Run("parent cancellation causes Canceled error", func(t *testing.T) {
				t.Parallel()

				tm := tmenginetest.NewTimeoutManager()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				require.Zero(t, tc.Outstanding(tm, 1, 0))

				tctx, tcancel := tc.Create(ctx, tm, 1, 0)
				defer tcancel()

				require.Equal(t, 1, tc.Outstanding(tm, 1, 0))

				cancel()
				require.Eventually(t, func() bool {
					return tc.Outstanding(tm, 1, 0) == 0
				}, 100*time.Millisecond, 2*time.Millisecond)

				require.Equal(t, context.Canceled, context.Cause(tctx))
				require.Zero(t, tc.Outstanding(tm, 1, 0))
			})

			t.Run("returned cancel func causes Canceled error", func(t *testing.T) {
				t.Parallel()

				tm := tmenginetest.NewTimeoutManager()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				require.Zero(t, tc.Outstanding(tm, 1, 0))

				tctx, tcancel := tc.Create(ctx, tm, 1, 0)
				defer tcancel()

				require.Equal(t, 1, tc.Outstanding(tm, 1, 0))

				tcancel()
				require.Eventually(t, func() bool {
					return tc.Outstanding(tm, 1, 0) == 0
				}, 100*time.Millisecond, 2*time.Millisecond)

				require.Equal(t, context.Canceled, context.Cause(tctx))
				require.Zero(t, tc.Outstanding(tm, 1, 0))
			})

			t.Run("triggering causes correct error", func(t *testing.T) {
				t.Parallel()

				tm := tmenginetest.NewTimeoutManager()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				require.Zero(t, tc.Outstanding(tm, 1, 0))

				tctx, tcancel := tc.Create(ctx, tm, 1, 0)
				defer tcancel()

				require.Equal(t, 1, tc.Outstanding(tm, 1, 0))

				tc.Trigger(tm, 1, 0)
				require.Eventually(t, func() bool {
					return tc.Outstanding(tm, 1, 0) == 0
				}, 100*time.Millisecond, 2*time.Millisecond)

				require.Equal(t, tc.WantErr, context.Cause(tctx))
				require.Zero(t, tc.Outstanding(tm, 1, 0))
			})
		})
	}
}
