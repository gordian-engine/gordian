package tmgossiptest

import "github.com/gordian-engine/gordian/tm/tmengine/tmelink"

type PassThroughStrategy struct {
	Ready chan struct{}

	Updates <-chan tmelink.NetworkViewUpdate
}

func NewPassThroughStrategy() *PassThroughStrategy {
	return &PassThroughStrategy{
		Ready: make(chan struct{}),
	}
}

func (s *PassThroughStrategy) Start(ch <-chan tmelink.NetworkViewUpdate) {
	s.Updates = ch
	close(s.Ready)
}

func (s *PassThroughStrategy) Wait() {}
