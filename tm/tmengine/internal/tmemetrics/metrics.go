package tmemetrics

import (
	"context"
	"fmt"
	"log/slog"
)

// Metrics is the set of metrics for an engine.
// This type is declared here, but aliased in [tmengine].
type Metrics struct {
	MirrorCommittingHeight uint64
	MirrorCommittingRound  uint32

	MirrorVotingHeight uint64
	MirrorVotingRound  uint32

	StateMachineHeight uint64
	StateMachineRound  uint32
}

func (m Metrics) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("mirror_committing_hr", fmt.Sprintf("%d/%d", m.MirrorCommittingHeight, m.MirrorCommittingRound)),

		slog.String("mirror_voting_hr", fmt.Sprintf("%d/%d", m.MirrorVotingHeight, m.MirrorVotingRound)),

		slog.String("state_machine_hr", fmt.Sprintf("%d/%d", m.StateMachineHeight, m.StateMachineRound)),
	)
}

type MirrorMetrics struct {
	// Voting.
	VH uint64
	VR uint32

	// Committing.
	CH uint64
	CR uint32
}

type StateMachineMetrics struct {
	H uint64
	R uint32
}

type Collector struct {
	mCh chan MirrorMetrics
	sCh chan StateMachineMetrics

	outCh chan<- Metrics

	done chan struct{}
}

func NewCollector(ctx context.Context, bufSize int, outCh chan<- Metrics) *Collector {
	c := &Collector{
		mCh: make(chan MirrorMetrics, bufSize),
		sCh: make(chan StateMachineMetrics, bufSize),

		outCh: outCh,

		done: make(chan struct{}),
	}
	go c.background(ctx)
	return c
}

func (c *Collector) UpdateMirror(m MirrorMetrics) {
	select {
	case c.mCh <- m:
	default:
	}
}

func (c *Collector) UpdateStateMachine(m StateMachineMetrics) {
	select {
	case c.sCh <- m:
	default:
	}
}

func (c *Collector) Wait() {
	<-c.done
}

func (c *Collector) background(ctx context.Context) {
	defer close(c.done)

	var cur Metrics

	var gotM, gotS, outdated bool
	for {
		// Don't attempt to send the output until
		// we've written both mirror and state machine metrics.
		var outCh chan<- Metrics
		if gotM && gotS && outdated {
			outCh = c.outCh
		}

		select {
		case <-ctx.Done():
			return

		case m := <-c.mCh:
			cur.MirrorCommittingHeight = m.CH
			cur.MirrorCommittingRound = m.CR
			cur.MirrorVotingHeight = m.VH
			cur.MirrorVotingRound = m.VR

			gotM = true
			outdated = true

		case s := <-c.sCh:
			cur.StateMachineHeight = s.H
			cur.StateMachineRound = s.R

			gotS = true
			outdated = true

		case outCh <- cur:
			// Okay.
			outdated = false
		}
	}
}
