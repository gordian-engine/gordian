package gexternalsigner

import (
	"context"
	"fmt"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var _ tmconsensus.Signer = ExternalSigner{}

const TODO_CHAIN_ID = "gordian"

// ExternalSigner is a [tmconsensus.Signer] that generates signatures
// by connecting to an external signer service.
type ExternalSigner struct {
	signer ExternalSignerClient
}

func NewExternalSigner(url string) (ExternalSigner, error) {
	// TODO support secure connections
	cc, err := grpc.NewClient(url, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return ExternalSigner{}, fmt.Errorf("NewExternalSigner failed to dial %s: %w", url, err)
	}

	return ExternalSigner{NewExternalSignerClient(cc)}, nil
}

func (s ExternalSigner) SignProposedHeader(ctx context.Context, ph *tmconsensus.ProposedHeader) error {
	res, err := s.signer.Propose(ctx, &ProposeRequest{
		ChainId: TODO_CHAIN_ID,
		Header: &Header{
			Height:           ph.Header.Height,
			PrevBlockHash:    ph.Header.PrevBlockHash,
			PrevAppStateHash: ph.Header.PrevAppStateHash,
			DataId:           ph.Header.DataID,
		},
		Round: ph.Round,
		Annotations: &Annotations{
			User:   ph.Annotations.User,
			Driver: ph.Annotations.Driver,
		},
	})
	if err != nil {
		return fmt.Errorf("ExternalSigner.Propose failed: %w", err)
	}

	ph.Signature = res.Signature
	return nil
}

func (s ExternalSigner) PubKey() gcrypto.PubKey {
	res, err := s.signer.PubKey(context.TODO(), &PubKeyRequest{
		ChainId: TODO_CHAIN_ID,
	})
	if err != nil {
		panic(fmt.Errorf("ExternalSigner.PubKey failed: %w", err))
	}

	// TODO support other key types
	return gcrypto.Ed25519PubKey(res.PubKey)
}

func (s ExternalSigner) Prevote(ctx context.Context, vt tmconsensus.VoteTarget) (
	signContent, signature []byte, err error,
) {
	res, err := s.signer.Prevote(ctx, &VoteRequest{
		ChainId:   TODO_CHAIN_ID,
		Height:    vt.Height,
		Round:     vt.Round,
		BlockHash: vt.BlockHash,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ExternalSigner.Prevote failed: %w", err)
	}

	return res.SignContent, res.Signature, nil
}

func (s ExternalSigner) Precommit(ctx context.Context, vt tmconsensus.VoteTarget) (
	signContent, signature []byte, err error,
) {
	res, err := s.signer.Prevote(ctx, &VoteRequest{
		ChainId:   TODO_CHAIN_ID,
		Height:    vt.Height,
		Round:     vt.Round,
		BlockHash: vt.BlockHash,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ExternalSigner.Precommit failed: %w", err)
	}

	return res.SignContent, res.Signature, nil
}
