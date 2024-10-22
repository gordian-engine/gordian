package tmenginetest

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/tm/internal/tmtimeout"
)

type hr struct {
	h uint64
	r uint32
}

type cc struct {
	Ctx    context.Context
	Cancel context.CancelCauseFunc
}

// TimeoutManager is an implementation of [tmengine.TimeoutManager]
// intended for use in tests that require a timeout manager,
// with fine control of when timeouts are triggered.
type TimeoutManager struct {
	mu sync.Mutex

	proposalTimeouts,
	prevoteDelayTimeouts,
	precommitDelayTimeouts,
	commitWaitTimeouts map[hr][]*cc
}

func NewTimeoutManager() *TimeoutManager {
	return &TimeoutManager{
		proposalTimeouts:       make(map[hr][]*cc),
		prevoteDelayTimeouts:   make(map[hr][]*cc),
		precommitDelayTimeouts: make(map[hr][]*cc),
		commitWaitTimeouts:     make(map[hr][]*cc),
	}
}

func (m *TimeoutManager) WithProposalTimeout(ctx context.Context, h uint64, r uint32) (context.Context, context.CancelFunc) {
	return m.withTimeout(ctx, m.proposalTimeouts, h, r)
}

func (m *TimeoutManager) OutstandingProposalTimeouts(h uint64, r uint32) int {
	return m.countOutstanding(m.proposalTimeouts, h, r)
}

func (m *TimeoutManager) TriggerProposalTimeout(h uint64, r uint32) int {
	return m.triggerTimeout(m.proposalTimeouts, h, r, tmtimeout.ErrProposalTimedOut)
}

func (m *TimeoutManager) WithPrevoteDelayTimeout(ctx context.Context, h uint64, r uint32) (context.Context, context.CancelFunc) {
	return m.withTimeout(ctx, m.prevoteDelayTimeouts, h, r)
}

func (m *TimeoutManager) OutstandingPrevoteDelayTimeouts(h uint64, r uint32) int {
	return m.countOutstanding(m.prevoteDelayTimeouts, h, r)
}

func (m *TimeoutManager) TriggerPrevoteDelayTimeout(h uint64, r uint32) int {
	return m.triggerTimeout(m.prevoteDelayTimeouts, h, r, tmtimeout.ErrPrevoteDelayTimedOut)
}

func (m *TimeoutManager) WithPrecommitDelayTimeout(ctx context.Context, h uint64, r uint32) (context.Context, context.CancelFunc) {
	return m.withTimeout(ctx, m.precommitDelayTimeouts, h, r)
}

func (m *TimeoutManager) OutstandingPrecommitDelayTimeouts(h uint64, r uint32) int {
	return m.countOutstanding(m.precommitDelayTimeouts, h, r)
}

func (m *TimeoutManager) TriggerPrecommitDelayTimeout(h uint64, r uint32) int {
	return m.triggerTimeout(m.precommitDelayTimeouts, h, r, tmtimeout.ErrPrecommitDelayTimedOut)
}

func (m *TimeoutManager) WithCommitWaitTimeout(ctx context.Context, h uint64, r uint32) (context.Context, context.CancelFunc) {
	return m.withTimeout(ctx, m.commitWaitTimeouts, h, r)
}

func (m *TimeoutManager) OutstandingCommitWaitTimeouts(h uint64, r uint32) int {
	return m.countOutstanding(m.commitWaitTimeouts, h, r)
}

func (m *TimeoutManager) TriggerCommitWaitTimeout(h uint64, r uint32) int {
	return m.triggerTimeout(m.commitWaitTimeouts, h, r, tmtimeout.ErrCommitWaitTimedOut)
}

func (m *TimeoutManager) triggerTimeout(mm map[hr][]*cc, h uint64, r uint32, e error) int {
	hr := hr{h: h, r: r}
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, c := range mm[hr] {
		if c != nil {
			c.Cancel(e)
			n++
		}
	}
	mm[hr] = nil

	return n
}

func (m *TimeoutManager) withTimeout(ctx context.Context, mm map[hr][]*cc, h uint64, r uint32) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)

	cc := &cc{Ctx: ctx, Cancel: cancel}
	hr := hr{h: h, r: r}

	m.mu.Lock()
	defer m.mu.Unlock()

	mm[hr] = append(mm[hr], cc)

	go m.clearAfterDone(mm, hr, cc)

	return ctx, func() {
		cancel(nil)
	}
}

func (m *TimeoutManager) clearAfterDone(mm map[hr][]*cc, hr hr, cc *cc) {
	<-cc.Ctx.Done()

	m.mu.Lock()
	defer m.mu.Unlock()

	for i, c := range mm[hr] {
		if c == cc {
			mm[hr][i] = nil
		}
	}
}

func (m *TimeoutManager) countOutstanding(mm map[hr][]*cc, h uint64, r uint32) int {
	hr := hr{h: h, r: r}

	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, c := range mm[hr] {
		if c != nil {
			n++
		}
	}

	return n
}
