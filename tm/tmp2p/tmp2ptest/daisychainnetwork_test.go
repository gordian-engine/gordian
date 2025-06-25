package tmp2ptest_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
)

func TestDaisyChainNetwork_Compliance(t *testing.T) {
	tmp2ptest.TestNetworkCompliance(
		t,
		func(t *testing.T, ctx context.Context) (tmp2ptest.Network, error) {
			n := tmp2ptest.NewDaisyChainNetwork(t, ctx)
			return &tmp2ptest.GenericNetwork[*tmp2ptest.DaisyChainConnection]{
				Network: n,
			}, nil
		},
	)
}
