package gsi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	abcitypes "github.com/cometbft/cometbft/api/cometbft/abci/v1"
	v1types "github.com/cometbft/cometbft/api/cometbft/types/v1"
	cmtp2p "github.com/cometbft/cometbft/p2p"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	tmtypes "github.com/cometbft/cometbft/types"
	"github.com/gorilla/mux"
)

type compatHandler struct {
	log *slog.Logger
}

func setCompatRoutes(log *slog.Logger, cfg HTTPServerConfig, r *mux.Router) {
	h := compatHandler{
		log: log,
	}

	r.HandleFunc("abci_info", h.HandleABCIInfo).Methods("GET")
	r.HandleFunc("abci_query", h.HandleABCIQuery).Methods("POST")
	r.HandleFunc("block", h.HandleBlock).Methods("POST")
	r.HandleFunc("block_by_hash", h.HandleBlockByHash).Methods("GET")
	r.HandleFunc("block_results", h.HandleBlockResults).Methods("POST")
	r.HandleFunc("block_search", h.HandleBlockSearch).Methods("POST")
	r.HandleFunc("blockchain", h.HandleBlockchainInfo).Methods("GET")
	r.HandleFunc("broadcast_evidence", h.HandleBroadcastEvidence).Methods("POST")
	r.HandleFunc("broadcast_tx_async", h.HandleBroadcastTxAsync).Methods("POST")
	r.HandleFunc("broadcast_tx_commit", h.HandleBroadcastTxCommit).Methods("POST")
	r.HandleFunc("broadcast_tx_sync", h.HandleBroadcastTxSync).Methods("POST")
	r.HandleFunc("check_tx", h.HandleCheckTx).Methods("POST")
	r.HandleFunc("commit", h.HandleCommit).Methods("POST")
	r.HandleFunc("consensus_params", h.HandleConsensusParams).Methods("GET")
	r.HandleFunc("consensus_state", h.HandleConsensusState).Methods("GET")
	r.HandleFunc("dump_consensus_state", h.HandleDumpConsensusState).Methods("GET")
	r.HandleFunc("genesis", h.HandleGenesis).Methods("GET")
	r.HandleFunc("genesis_chunked", h.HandleGenesisChunked).Methods("GET")
	r.HandleFunc("header", h.HandleHeader).Methods("GET")
	r.HandleFunc("header_by_hash", h.HandleHeaderByHash).Methods("GET")
	r.HandleFunc("health", h.HandleHealth).Methods("GET")
	r.HandleFunc("net_info", h.HandleNetInfo).Methods("GET")
	r.HandleFunc("num_unconfirmed_txs", h.HandleNumUnconfirmedTxs).Methods("GET")
	r.HandleFunc("status", h.HandleStatus).Methods("GET")
	r.HandleFunc("subscribe", h.HandleSubscribe).Methods("GET")
	r.HandleFunc("tx", h.HandleTx).Methods("POST")
	r.HandleFunc("tx_search", h.HandleTxSearch).Methods("GET")
	r.HandleFunc("unconfirmed_txs", h.HandleUnconfirmedTxs).Methods("GET")
	r.HandleFunc("unsubscribe", h.HandleUnsubscribe).Methods("GET")
	r.HandleFunc("unsubscribe_all", h.HandleUnsubscribeAll).Methods("GET")
	r.HandleFunc("validators", h.HandleValidators).Methods("GET")
}

func (h compatHandler) HandleABCIInfo(w http.ResponseWriter, req *http.Request) {
	_ = ctypes.ResultABCIInfo{
		Response: abcitypes.InfoResponse{
			Data:             "",
			Version:          "",
			AppVersion:       0,
			LastBlockHeight:  0,
			LastBlockAppHash: []byte{},
		},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleABCIQuery(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultABCIQuery{
		Response: abcitypes.QueryResponse{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBlock(w http.ResponseWriter, req *http.Request) {
	// Request
	// height *int64
	_ = &ctypes.ResultBlock{
		BlockID: tmtypes.BlockID{},
		Block:   &tmtypes.Block{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBlockByHash(w http.ResponseWriter, req *http.Request) {
	// Request
	// hash []byte
	_ = &ctypes.ResultBlock{
		BlockID: tmtypes.BlockID{},
		Block:   &tmtypes.Block{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBlockResults(w http.ResponseWriter, req *http.Request) {
	// Request
	// height *int64
	_ = &ctypes.ResultBlockResults{
		Height:                0,
		TxResults:             []*abcitypes.ExecTxResult{},
		FinalizeBlockEvents:   []abcitypes.Event{},
		ValidatorUpdates:      []abcitypes.ValidatorUpdate{},
		ConsensusParamUpdates: &v1types.ConsensusParams{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBlockSearch(w http.ResponseWriter, req *http.Request) {
	// Request
	// query string
	// cmtquery.New(query)
	// cmtquery "github.com/cometbft/cometbft/libs/pubsub/query"
	_ = &ctypes.ResultBlockSearch{
		Blocks:     []*ctypes.ResultBlock{},
		TotalCount: 0,
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBlockchainInfo(w http.ResponseWriter, req *http.Request) {
	// Request
	// minHeight *int64
	// maxHeight *int64
	_ = &ctypes.ResultBlockchainInfo{
		LastHeight: 0,
		BlockMetas: []*tmtypes.BlockMeta{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBroadcastEvidence(w http.ResponseWriter, req *http.Request) {
	// Request
	// Evidence *tmtypes.Evidence TODO
	_ = &ctypes.ResultBroadcastEvidence{
		Hash: []byte{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBroadcastTxAsync(w http.ResponseWriter, req *http.Request) {
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBroadcastTxCommit(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultBroadcastTxCommit{
		CheckTx:  abcitypes.CheckTxResponse{},
		TxResult: abcitypes.ExecTxResult{},
		Hash:     []byte{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleBroadcastTxSync(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultBroadcastTx{}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleCheckTx(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultCheckTx{}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleCommit(w http.ResponseWriter, req *http.Request) {
	// Request
	// height *int64
	_ = ctypes.NewResultCommit(&tmtypes.Header{}, &tmtypes.Commit{}, true)
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleConsensusParams(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultConsensusParams{
		BlockHeight:     0,
		ConsensusParams: tmtypes.ConsensusParams{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleConsensusState(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultConsensusState{
		RoundState: json.RawMessage{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleDumpConsensusState(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultDumpConsensusState{
		RoundState: json.RawMessage{},
		Peers:      []ctypes.PeerStateInfo{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleGenesis(w http.ResponseWriter, req *http.Request) {
	_ = ctypes.ResultGenesis{}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleGenesisChunked(w http.ResponseWriter, req *http.Request) {
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleHeader(w http.ResponseWriter, req *http.Request) {
	// Request
	// height *int64
	_ = &ctypes.ResultHeader{
		Header: &tmtypes.Header{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleHeaderByHash(w http.ResponseWriter, req *http.Request) {
	// Request
	// hash []byte
	_ = &ctypes.ResultHeader{
		Header: &tmtypes.Header{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleHealth(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultHealth{}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleNetInfo(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultNetInfo{
		Listening: false,
		Listeners: []string{},
		NPeers:    0,
		Peers:     []ctypes.Peer{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleNumUnconfirmedTxs(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultUnconfirmedTxs{
		Count:      0,
		Total:      0,
		TotalBytes: 0,
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleStatus(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultStatus{
		NodeInfo: cmtp2p.DefaultNodeInfo{
			ProtocolVersion: cmtp2p.ProtocolVersion{
				P2P:   0,
				Block: 0,
				App:   0,
			},
			DefaultNodeID: "",
			ListenAddr:    "",
			Network:       "",
			Version:       "",
			Channels:      []byte{},
			Moniker:       "",
			Other:         cmtp2p.DefaultNodeInfoOther{},
		},
		SyncInfo: ctypes.SyncInfo{

			LatestBlockHash:     []byte{},
			LatestAppHash:       []byte{},
			LatestBlockHeight:   0,
			LatestBlockTime:     time.Now(),
			EarliestBlockHash:   []byte{},
			EarliestAppHash:     []byte{},
			EarliestBlockHeight: 0,
			EarliestBlockTime:   time.Now(),
			CatchingUp:          false,
		},
		ValidatorInfo: ctypes.ValidatorInfo{
			Address: []byte{},
			// PubKey: tmcrypto.NewPubKey(),
			VotingPower: 0,
		},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleSubscribe(w http.ResponseWriter, req *http.Request) {
	// TODO: subscribe to events via a websocket. Would be nice to add this feature as some interfaces rely on it
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleTx(w http.ResponseWriter, req *http.Request) {
	// Request
	// hash []byte
	// prove bool
	_ = &ctypes.ResultTx{
		Hash:     []byte{},
		Height:   0,
		TxResult: abcitypes.ExecTxResult{},
		Tx:       tmtypes.Tx{},
		Proof:    tmtypes.TxProof{},
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleTxSearch(w http.ResponseWriter, req *http.Request) {
	// Request
	// query string
	// prove bool
	// page int
	// perPage int
	// orderBy string
	_ = &ctypes.ResultTxSearch{
		Txs:        []*ctypes.ResultTx{},
		TotalCount: 0,
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleUnconfirmedTxs(w http.ResponseWriter, req *http.Request) {
	_ = &ctypes.ResultUnconfirmedTxs{
		Count:      0,
		Total:      0,
		Txs:        []tmtypes.Tx{},
		TotalBytes: 0,
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleUnsubscribe(w http.ResponseWriter, req *http.Request) {
	// TODO: subscribe to events via a websocket. Would be nice to add this feature as some interfaces rely on it
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleUnsubscribeAll(w http.ResponseWriter, req *http.Request) {
	// TODO: subscribe to events via a websocket. Would be nice to add this feature as some interfaces rely on it
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}

func (h compatHandler) HandleValidators(w http.ResponseWriter, req *http.Request) {
	// Request
	// height *int64
	// page int
	// perPage int
	_ = &ctypes.ResultValidators{
		BlockHeight: 0,
		Validators:  []*tmtypes.Validator{},
		Count:       0,
		Total:       0,
	}
	http.Error(w, "not yet implemented", http.StatusNotImplemented)
}
