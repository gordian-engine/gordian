package tmlibp2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/gexchange"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/tm/tmcodec"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmp2p"
)

const topicConsensus = "consensus/v1"

// Connection is a connection to a libp2p network,
// including appropriate pubsub subscriptions.
type Connection struct {
	log *slog.Logger

	codec tmcodec.MarshalCodec

	h       *Host
	dhtPeer *dht.IpfsDHT

	consensusTopic *pubsub.Topic
	consensusSub   *pubsub.Subscription

	outgoingProposals chan tmconsensus.ProposedBlock

	outgoingPrevoteProofs   chan tmconsensus.PrevoteSparseProof
	outgoingPrecommitProofs chan tmconsensus.PrecommitSparseProof

	setConsensusHandlerRequests chan setConsensusHandlerRequest

	wg sync.WaitGroup

	disconnectOnce sync.Once
	disconnected   chan struct{}
}

// NewConnection returns a new Connection based on
// a host that has already joined a network.
func NewConnection(ctx context.Context, log *slog.Logger, h *Host, codec tmcodec.MarshalCodec) (*Connection, error) {
	consensusTopic, err := h.PubSub().Join(topicConsensus)
	if err != nil {
		return nil, err
	}

	consensusSub, err := consensusTopic.Subscribe()
	if err != nil {
		return nil, err
	}

	dhtPeer, err := dht.New(
		ctx,
		h.Libp2pHost(),

		dht.ProtocolPrefix("/gordian"), // TODO: maybe this should not be hardcoded.
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DHT peer: %w", err)
	}

	c := &Connection{
		log: log,

		codec: codec,

		h:       h,
		dhtPeer: dhtPeer,

		consensusTopic: consensusTopic,
		consensusSub:   consensusSub,

		outgoingProposals: make(chan tmconsensus.ProposedBlock, 1),

		outgoingPrevoteProofs:   make(chan tmconsensus.PrevoteSparseProof, 1),
		outgoingPrecommitProofs: make(chan tmconsensus.PrecommitSparseProof, 1),

		setConsensusHandlerRequests: make(chan setConsensusHandlerRequest, 1),

		disconnected: make(chan struct{}),
	}

	// Ensure that the subscriptions are ready,
	// as their setup happens in the background.
	waitForSubscriptions(h.PubSub(), topicConsensus)

	c.wg.Add(2)
	go c.background(ctx)
	go c.drainSub(ctx, consensusSub)

	return c, nil
}

func (c *Connection) background(ctx context.Context) {
	defer c.wg.Done()

	if err := c.h.PubSub().RegisterTopicValidator(topicConsensus, ignoreMessage); err != nil {
		c.log.Warn("Failed to initialize consensus topic validator", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			// The connection lifecycle has terminated, so quit.
			return

		case pb, ok := <-c.outgoingProposals:
			// A proposal that should go out to the network.

			if !ok {
				// Channel was closed; we're quitting.
				return
			}

			b, err := c.codec.MarshalConsensusMessage(tmcodec.ConsensusMessage{
				ProposedBlock: &pb,
			})
			if err != nil {
				c.log.Warn("Failed to marshal consensus message for proposed block; cannot broadcast value to network", "err", err)
				continue
			}

			if err := c.consensusTopic.Publish(ctx, b); err != nil {
				c.log.Warn("Failed to publish proposed block", "err", err)
			}
		case p, ok := <-c.outgoingPrevoteProofs:
			// A prevote proof that should go out to the network.

			if !ok {
				// Channel was closed; we're quitting.
				return
			}

			b, err := c.codec.MarshalConsensusMessage(tmcodec.ConsensusMessage{
				PrevoteProof: &p,
			})
			if err != nil {
				c.log.Warn("Failed to marshal consensus message for prevote proof; cannot broadcast value to network", "err", err)
				continue
			}

			if err := c.consensusTopic.Publish(ctx, b); err != nil {
				c.log.Warn("Failed to publish prevote proof", "err", err)
			}
		case p, ok := <-c.outgoingPrecommitProofs:
			// A precommit that should go out to the network.

			if !ok {
				// Channel was closed; we're quitting.
				return
			}

			b, err := c.codec.MarshalConsensusMessage(tmcodec.ConsensusMessage{
				PrecommitProof: &p,
			})
			if err != nil {
				c.log.Warn("Failed to marshal consensus message for precommit proof; cannot broadcast value to network", "err", err)
				continue
			}

			if err := c.consensusTopic.Publish(ctx, b); err != nil {
				c.log.Warn("Failed to publish precommit proof", "err", err)
			}

		case req := <-c.setConsensusHandlerRequests:
			// There is always a topic validator, so unregister the previous one.
			if err := c.h.PubSub().UnregisterTopicValidator(topicConsensus); err != nil {
				c.log.Warn("Failed to unregister previous topic validator for consensus messages", "err", err)
			}

			// NOTE: there is a potential race right here,
			// where we temporarily have no topic validator set,
			// between removing and replacing it.
			//
			// Unfortunately it doesn't look like there is a way to atomically swap the validator,
			// nor is there an obvious way to leave the topic and
			// instantaneously join it while setting a validator.
			//
			// Perhaps the alternative is to have a fixed method as the topic validator,
			// and use sync/atomic to swap the handler.

			// Always reassign a topic validator.
			if req.Handler == nil {
				if err := c.h.PubSub().RegisterTopicValidator(topicConsensus, ignoreMessage); err != nil {
					c.log.Warn("Failed to register consensus topic validator when clearing handler", "err", err)
				}
			} else {
				if err := c.h.PubSub().RegisterTopicValidator(
					topicConsensus,
					c.libp2pConsensusMessageValidator(req.Handler),
				); err != nil {
					c.log.Warn("Failed to register topic validator for consensus messages", "err", err)
				}
			}

			close(req.Ready)
		}
	}
}

// ignoreMessage is a pubsub validator that ignores all incoming messages.
// This is useful as a default strategy before (*Connection).SetConsensusHandler is called.
func ignoreMessage(context.Context, peer.ID, *pubsub.Message) pubsub.ValidationResult {
	return pubsub.ValidationIgnore
}

// libp2pConsensusMessageValidator returns a pubsub validator for the consensus message topic.
//
// This callback is run on every new pubsub message,
// so it needs to be associated with a consensus engine in order to act on the message
// and return the decision of whether the message should continue to propagate
// through the p2p network.
func (c *Connection) libp2pConsensusMessageValidator(
	h tmconsensus.ConsensusHandler,
) pubsub.ValidatorEx {
	selfID := c.h.Libp2pHost().ID()
	return func(ctx context.Context, id peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
		if id == selfID {
			// Don't process a message we sent,
			// as our local state must have already been consistent with this message
			// before we sent it.
			return pubsub.ValidationAccept
		}

		var cm tmcodec.ConsensusMessage
		if err := c.codec.UnmarshalConsensusMessage(msg.Data, &cm); err != nil {
			c.log.Info("Failed to unmarshal data into consensus message", "err", err)
			return pubsub.ValidationIgnore
		}

		var f gexchange.Feedback
		switch {
		case cm.ProposedBlock != nil && h != nil:
			f = h.HandleProposedBlock(ctx, *cm.ProposedBlock)
		case cm.PrevoteProof != nil && h != nil:
			f = h.HandlePrevoteProofs(ctx, *cm.PrevoteProof)
		case cm.PrecommitProof != nil && h != nil:
			f = h.HandlePrecommitProofs(ctx, *cm.PrecommitProof)
		default:
			// Undefined behavior if no field was set,
			// so in this case reject it.
			f = gexchange.FeedbackRejected
		}
		return c.exchangeFeedbackToLibp2p(f)
	}
}

func (c *Connection) exchangeFeedbackToLibp2p(f gexchange.Feedback) pubsub.ValidationResult {
	switch f {
	case gexchange.FeedbackAccepted:
		return pubsub.ValidationAccept
	case gexchange.FeedbackRejected:
		return pubsub.ValidationReject
	case gexchange.FeedbackIgnored:
		return pubsub.ValidationIgnore
	default:
		c.log.Info("Handler returned unacceptable feedback value", "f", f)
		return pubsub.ValidationIgnore
	}
}

// drainSub continually reads from a subscription.
func (c *Connection) drainSub(ctx context.Context, sub *pubsub.Subscription) {
	// NOTE: without a subscription, there is no apparent way to check when a topic is fully joined.
	// Without that check, there is no way to synchronize the start of a test to multiple hosts
	// being able to communicate over the same channel.
	//
	// It would seem like simply joining the topic would suffice,
	// so maybe that is something that can be improved in the future.
	defer c.wg.Done()

	for {
		_, err := sub.Next(ctx)
		if err != nil {
			// From reading the source, it looks like err will be non-nil
			// upon context cancellation or if the subscription is canceled.
			// Neither of those are log-worthy.
			// Moreover, canceling the subscription in test can result in a rare
			// log after test finish, causing a spurious data race and test failure.
			if err != context.Canceled && !errors.Is(err, pubsub.ErrSubscriptionCancelled) {
				c.log.Info("Quitting subscription draining due to error", "err", err)
			}
			return
		}
	}
}

// ConsensusBroadcaster returns c, which already satisfies the ConsensusBroadcaster interface.
func (c *Connection) ConsensusBroadcaster() tmp2p.ConsensusBroadcaster {
	return c
}

// OutgoingPrevoteProofs returns a channel where prevote proofs may be sent,
// after which they will be broadcast to the p2p network.
func (c *Connection) OutgoingPrevoteProofs() chan<- tmconsensus.PrevoteSparseProof {
	return c.outgoingPrevoteProofs
}

// OutgoingPrecommitProofs returns a channel where precommits may be sent,
// after which they will be broadcast to the p2p network.
func (c *Connection) OutgoingPrecommitProofs() chan<- tmconsensus.PrecommitSparseProof {
	return c.outgoingPrecommitProofs
}

// OutgoingProposedBlocks returns a channel where proposed blocks may be sent,
// after which they will be broadcast to the p2p network.
func (c *Connection) OutgoingProposedBlocks() chan<- tmconsensus.ProposedBlock {
	return c.outgoingProposals
}

func (c *Connection) Disconnect() {
	c.disconnectOnce.Do(func() {
		// Unregister the topic validators.
		// This doesn't seem necessary, but sometimes during tests,
		// we will get a late log message after the test has failed,
		// perhaps due to other resources not being cleaned up properly.
		_ = c.h.PubSub().UnregisterTopicValidator(topicConsensus)

		c.consensusSub.Cancel()
		if err := c.consensusTopic.Close(); err != nil && err != context.Canceled {
			c.log.Info("Error closing consensus message topic during disconnect", "err", err)
		}

		if err := c.h.Close(); err != nil {
			c.log.Info("Error closing connection host", "err", err)
		}

		close(c.disconnected)
	})
}

// Disconnected returns a channel that is closed once
// c.Disconnect() has been called and has returned.
func (c *Connection) Disconnected() <-chan struct{} {
	return c.disconnected
}

// Host returns c's underlying Host.
// This is useful for some bookkeeping in [tmlibp2ptest.Network].
func (c *Connection) Host() *Host {
	return c.h
}

// Codec returns c's codec.
// This is primarily useful for testing.
func (c *Connection) Codec() tmcodec.MarshalCodec {
	return c.codec
}

// SetConsensusHandler sets the consensus handler for this Connection.
// h may be nil to ignore consensus messages.
func (c *Connection) SetConsensusHandler(ctx context.Context, h tmconsensus.ConsensusHandler) {
	ready := make(chan struct{})
	req := setConsensusHandlerRequest{
		Handler: h,
		Ready:   ready,
	}

	_, _ = gchan.ReqResp(
		ctx, c.log,
		c.setConsensusHandlerRequests, req,
		req.Ready,
		"setting consensus handler",
	)
}

type setConsensusHandlerRequest struct {
	Handler tmconsensus.ConsensusHandler
	Ready   chan struct{}
}

// WaitForSubscriptions checks for reported subscriptions from ps.
// If the reported subscriptions do not include every topic in topics
// within an arbitrary three seconds, it returns an error.
//
// There is no synchonous callback to discover when a subscription is ready,
// so we are left with polling for this.
func waitForSubscriptions(ps *pubsub.PubSub, topics ...string) error {
	// Arbitrary deadline of 3s.
	// TODO: This probably should use context instead.
	deadline := time.Now().Add(3 * time.Second)

	var have []string
OUTER:
	for time.Now().Before(deadline) {
		have = ps.GetTopics()
		if len(have) < len(topics) {
			// Not enough topics.
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Enough topics. Is every wanted topic present?
		for _, t := range topics {
			if !slices.Contains(have, t) {
				time.Sleep(10 * time.Millisecond)
				continue OUTER
			}
		}

		// Have all topics within deadline.
		return nil
	}

	return fmt.Errorf(
		"not all subscriptions ready within 3s; have: %s; want: %s",
		strings.Join(have, ", "),
		strings.Join(topics, ", "),
	)
}
