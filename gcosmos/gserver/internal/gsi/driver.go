package gsi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	coreappmgr "cosmossdk.io/core/app"
	corecomet "cosmossdk.io/core/comet"
	corecontext "cosmossdk.io/core/context"
	"cosmossdk.io/core/store"
	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"cosmossdk.io/store/v2/root"
	consensustypes "cosmossdk.io/x/consensus/types"
	cometapitypes "github.com/cometbft/cometbft/api/cometbft/types/v1"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/rollchains/gordian/gcosmos/gccodec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/txmanager"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gdriver/gdatapool"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
)

type DriverConfig struct {
	ConsensusAuthority string

	AppManager *appmanager.AppManager[transaction.Tx]

	Store *root.Store

	InitChainRequests     <-chan tmdriver.InitChainRequest
	FinalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest

	TxBuffer *txmanager.SDKTxBuf

	DataPool *gdatapool.Pool[[]transaction.Tx]
}

type Driver struct {
	log *slog.Logger

	txBuf *txmanager.SDKTxBuf

	pool *gdatapool.Pool[[]transaction.Tx]

	done chan struct{}
}

func NewDriver(
	lifeCtx, valCtx context.Context,
	log *slog.Logger,
	cfg DriverConfig,
) (*Driver, error) {
	cc := valCtx.Value(client.ClientContextKey).(*client.Context)
	serverCtx := valCtx.Value(server.ServerContextKey).(*server.Context)

	genesisPath := filepath.Join(cc.HomeDir, serverCtx.Config.Genesis)

	ag, err := genutiltypes.AppGenesisFromFile(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("failed to run AppGenesisFromFile(%s): %w", genesisPath, err)
	}

	d := &Driver{
		log: log,

		txBuf: cfg.TxBuffer,

		pool: cfg.DataPool,

		done: make(chan struct{}),
	}

	go d.run(lifeCtx, ag, cc.TxConfig, cfg)

	return d, nil
}

func (d *Driver) run(
	lifeCtx context.Context,
	ag *genutiltypes.AppGenesis,
	txConfig client.TxConfig,
	cfg DriverConfig,
) {
	defer close(d.done)

	d.log.Info("Driver starting...")
	defer d.log.Info("Driver goroutine finished")

	// We are currently assuming we always need to handle init chain,
	// but we should handle non-initial height.
	if !d.handleInitialization(
		lifeCtx,
		ag,
		cfg.ConsensusAuthority,
		cfg.AppManager,
		cfg.Store,
		txConfig,
		cfg.InitChainRequests,
	) {
		return
	}

	d.handleFinalizations(lifeCtx, cfg.AppManager, cfg.Store, cfg.FinalizeBlockRequests)
}

func (d *Driver) handleInitialization(
	lifeCtx context.Context,
	ag *genutiltypes.AppGenesis,
	consensusAuthority string,
	appManager *appmanager.AppManager[transaction.Tx],
	s *root.Store,
	txConfig client.TxConfig,
	initChainCh <-chan tmdriver.InitChainRequest,
) bool {
	req, ok := gchan.RecvC(lifeCtx, d.log, initChainCh, "receiving init chain request")
	if !ok {
		d.log.Warn("Context cancelled before receiving init chain message")
		return false
	}
	d.log.Info("Got init chain request", "val", req)

	blockReq := &coreappmgr.BlockRequest[transaction.Tx]{
		Height: req.Genesis.InitialHeight, // Comet does genesis height - 1, do we need that too?

		ChainId:   req.Genesis.ChainID,
		IsGenesis: true,

		// TODO: these should be actually calculated.
		// Right now they are just appropriate sized to get past initial validation inside the SDK.
		Hash:    make([]byte, 32),
		AppHash: make([]byte, 32),

		// Omitted vs comet server code: Time, Hash, AppHash
	}

	appState := []byte(ag.AppState)

	// Now, init genesis in the SDK-layer application.
	var codec transaction.Codec[transaction.Tx] = gccodec.NewTxDecoder(txConfig)

	// We need a special context for the InitGenesis call,
	// as the consensus parameters are expected to be a value on the context there.
	deliverCtx := context.WithValue(lifeCtx, corecontext.InitInfoKey, &consensustypes.MsgUpdateParams{
		Authority: consensusAuthority,
		Block: &cometapitypes.BlockParams{
			// Just setting these to something non-zero for now.
			MaxBytes: -1,
			MaxGas:   -1,
		},
		Evidence: &cometapitypes.EvidenceParams{
			// Completely arbitrary non-zero values.
			MaxAgeNumBlocks: 10,
			MaxAgeDuration:  10 * time.Minute,
			MaxBytes:        1024,
		},
		Validator: &cometapitypes.ValidatorParams{
			PubKeyTypes: []string{"ed25519"},
		},
	})
	blockResp, genesisState, err := appManager.InitGenesis(
		deliverCtx, blockReq, appState, codec,
	)
	if err != nil {
		d.log.Warn("Failed to run appManager.InitGenesis", "appState", fmt.Sprintf("%q", appState), "err", err)
		return false
	}

	// Set the initial state on the transaction buffer.
	d.txBuf.Initialize(lifeCtx, genesisState)

	// SetInitialVersion followed by WorkingHash,
	// and passing that hash as the initial app state hash,
	// is what the comet server code does.
	if err := s.SetInitialVersion(req.Genesis.InitialHeight); err != nil {
		d.log.Warn("Failed to set initial version", "err", err)
		return false
	}

	stateChanges, err := genesisState.GetStateChanges()
	if err != nil {
		d.log.Warn("Failed to set get state changes from genesis state", "err", err)
		return false
	}
	stateRoot, err := s.WorkingHash(&store.Changeset{
		Changes: stateChanges,
	})
	if err != nil {
		d.log.Warn("Failed to get working hash from changeset", "err", err)
		return false
	}

	gVals := make([]tmconsensus.Validator, len(blockResp.ValidatorUpdates))
	for i, vu := range blockResp.ValidatorUpdates {
		if vu.PubKeyType != "ed25519" {
			panic(fmt.Errorf(
				"TODO: handle validator with non-ed25519 key type: %q",
				vu.PubKeyType,
			))
		}
		pk, err := gcrypto.NewEd25519PubKey(vu.PubKey)
		if err != nil {
			panic(fmt.Errorf(
				"BUG: NewEd25519PubKey should never error; got %w", err,
			))
		}
		gVals[i] = tmconsensus.Validator{
			PubKey: pk,
			Power:  uint64(vu.Power),
		}
	}

	resp := tmdriver.InitChainResponse{
		AppStateHash: stateRoot,

		Validators: gVals,
	}
	if !gchan.SendC(
		lifeCtx, d.log,
		req.Resp, resp,
		"sending init chain response to Gordian",
	) {
		// If this failed it will have logged, so we can just return here.
		return false
	}

	d.log.Info(
		"Successfully sent init chain response to Gordian engine",
		"n_vals", len(resp.Validators),
		"app_state_hash", glog.Hex(resp.AppStateHash),
	)

	return true
}

func (d *Driver) handleFinalizations(
	ctx context.Context,
	appManager *appmanager.AppManager[transaction.Tx],
	s *root.Store,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) {
	for {
		fbReq, ok := gchan.RecvC(
			ctx, d.log,
			finalizeBlockRequests,
			"receiving finalize block request from engine",
		)
		if !ok {
			// Context was cancelled and we already logged, so we're done.
			return
		}

		// TODO: the comet implementation does some validation and checking for halt height and time,
		// which we are not yet doing.

		// TODO: don't hardcode the initial height.
		const initialHeight = 1
		if fbReq.Block.Height == initialHeight {
			appHash, err := s.Commit(store.NewChangeset())
			if err != nil {
				d.log.Warn("Failed to commit new changeset for initial height", "err", err)
				return
			}

			resp := tmdriver.FinalizeBlockResponse{
				Height:    fbReq.Block.Height,
				Round:     fbReq.Round,
				BlockHash: nil, // TODO

				// At genesis, we don't have a block response
				// from which to extract the next validators.
				// By design, the validators at height 2 match height 1.
				Validators:   fbReq.Block.NextValidators,
				AppStateHash: appHash,
			}
			if !gchan.SendC(
				ctx, d.log,
				fbReq.Resp, resp,
				"sending finalize block response back to engine",
			) {
				// Context was cancelled and we already logged, so we're done.
				return
			}

			continue
		}

		cID, err := s.LastCommitID()
		if err != nil {
			d.log.Warn(
				"Failed to get last commit ID prior to handling finalization request",
				"err", err,
			)
			return
		}

		a, err := BlockAnnotationFromBytes(fbReq.Block.Annotations.Driver)
		if err != nil {
			d.log.Warn(
				"Failed to extract driver annotation from finalize block request",
				"err", err,
			)
			return
		}
		blockTime, err := a.BlockTimeAsTime()
		if err != nil {
			d.log.Warn(
				"Failed to parse block time from driver annotation",
				"err", err,
			)
			return
		}

		var txs []transaction.Tx

		// Guard the call to pool.Have with a zero check,
		// because we never call Need or SetAvailable on zero.
		if !gsbd.IsZeroTxDataID(string(fbReq.Block.DataID)) {
			haveTxs, err, have := d.pool.Have(string(fbReq.Block.DataID))
			if !have {
				panic(errors.New("TODO: handle block data not ready during finalization"))
			}
			if err != nil {
				panic(fmt.Errorf("TODO: handle error on retrieved block data during finalization: %w", err))
			}
			txs = haveTxs
		}

		blockReq := &coreappmgr.BlockRequest[transaction.Tx]{
			Height: fbReq.Block.Height,

			Time: blockTime,

			Hash:    fbReq.Block.Hash,
			AppHash: cID.Hash,
			ChainId: "???", // TODO: the chain ID needs to be threaded here properly.
			Txs:     txs,
		}

		// The app manager requires that comet info is set on its context
		// when dealing with a delivered block.
		ctx = context.WithValue(ctx, corecontext.CometInfoKey, corecomet.Info{
			// Somewhat surprisingly, just the presence of the key
			// without any values populated, is enough to progress past the panic.
		})

		// The discarded response value appears to include events
		// which we are not yet using.
		blockResp, newState, err := appManager.DeliverBlock(ctx, blockReq)
		if err != nil {
			d.log.Warn(
				"Failed to deliver block",
				"height", blockReq.Height,
				"err", err,
			)
			return
		}

		// By default, we just use the block's declared next validators block.
		nextVals := fbReq.Block.NextValidators
		if len(blockResp.ValidatorUpdates) > 0 {
			// Unclear if this is strictly necessary,
			// but just be defensive here anyway.
			// Since validators are just plain structs with a power and a pubkey,
			// cloning this slice means updates to this slice
			// do not affect the original.
			nextVals = slices.Clone(nextVals)

			// Make a map of pubkeys that have a power change.
			// TODO: this doesn't respect key type, and it should.
			var valsToUpdate = make(map[string]uint64)
			for _, vu := range blockResp.ValidatorUpdates {
				// TODO: vu.Power is an int64, and we are casting it to uint64 here.
				// There needs to be a safety check on conversion.
				valsToUpdate[string(vu.PubKey)] = uint64(vu.Power)
			}

			// Now iterate over all the validators, applying the new powers.
			for i := range nextVals {
				// Is the current validator in the update map?
				newPow, ok := valsToUpdate[string(nextVals[i].PubKey.PubKeyBytes())]
				if !ok {
					continue
				}

				// Yes, so reassign its power.
				nextVals[i].Power = newPow

				// Delete this entry.
				delete(valsToUpdate, string(nextVals[i].PubKey.PubKeyBytes()))

				// Stop iterating if this was the last entry.
				if len(valsToUpdate) == 0 {
					break
				}
			}

			if len(valsToUpdate) > 0 {
				panic(fmt.Errorf(
					"after applying validator updates, had %d update(s) left over",
					len(valsToUpdate),
				))
			}
		}

		// Rebase the transactions on the new state.
		// For now, we just discard the invalidated transactions.
		// We could log them but it isn't clear what exactly should be logged.
		// Maybe the transaction hash would suffice?
		if _, err := d.txBuf.Rebase(ctx, newState, txs); err != nil {
			d.log.Warn("Failed to rebase transaction buffer", "err", err)
			return
		}

		stateChanges, err := newState.GetStateChanges()
		if err != nil {
			d.log.Warn("Failed to get state changes", "err", err)
			return
		}
		appHash, err := s.Commit(&store.Changeset{Changes: stateChanges})
		if err != nil {
			d.log.Warn("Failed to commit state changes", "err", err)
			return
		}
		d.log.Info("Committed change to root store", "height", fbReq.Block.Height, "apphash", glog.Hex(appHash))

		// TODO: There could be updated consensus params that we care about here.

		fbResp := tmdriver.FinalizeBlockResponse{
			Height:    fbReq.Block.Height,
			Round:     fbReq.Round,
			BlockHash: fbReq.Block.Hash,

			Validators: nextVals,

			AppStateHash: appHash,
		}
		if !gchan.SendC(
			ctx, d.log,
			fbReq.Resp, fbResp,
			"sending finalize block response back to engine",
		) {
			// Context was cancelled and we already logged, so we're done.
			return
		}
	}
}

func (d *Driver) Wait() {
	<-d.done
}
