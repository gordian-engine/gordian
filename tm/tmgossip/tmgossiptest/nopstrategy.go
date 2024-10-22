package tmgossiptest

import "github.com/gordian-engine/gordian/tm/tmengine/tmelink"

// NopStrategy is a no-op [tmgossip.Strategy3a] for use in tests
// where a placeholder strategy is needed.
type NopStrategy struct{}

func (NopStrategy) Start(<-chan tmelink.NetworkViewUpdate) {}
func (NopStrategy) Wait()                                  {}
