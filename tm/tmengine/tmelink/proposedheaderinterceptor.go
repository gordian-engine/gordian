package tmelink

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ProposedHeaderInterceptor is called after the proposed header is prepared,
// but before the block hash is calculated and the proposed header is signed.
//
// This gives the driver an opportunity to modify annotations,
// or to otherwise detect when the current process is creating the proposed header.
type ProposedHeaderInterceptor interface {
	InterceptProposedHeader(context.Context, *tmconsensus.ProposedHeader) error
}

// ProposedHeaderInterceptorFunc allows converting a standalone function
// into a [ProposedHeaderInterceptor].
type ProposedHeaderInterceptorFunc func(context.Context, *tmconsensus.ProposedHeader) error

// InterceptProposedHeader implements [ProposedHeaderInterceptor].
func (f ProposedHeaderInterceptorFunc) InterceptProposedHeader(
	ctx context.Context, ph *tmconsensus.ProposedHeader,
) error {
	return f(ctx, ph)
}
