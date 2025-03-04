package chain

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/lightninglabs/neutrino/query"
	"github.com/lightningnetwork/lnd/ticker"
	"github.com/tinhnguyenhn/colxd/blockchain"
	"github.com/tinhnguyenhn/colxd/btcjson"
	"github.com/tinhnguyenhn/colxd/chaincfg"
	"github.com/tinhnguyenhn/colxd/chaincfg/chainhash"
	"github.com/tinhnguyenhn/colxd/peer"
	"github.com/tinhnguyenhn/colxd/wire"
	"github.com/tinhnguyenhn/colxutil"
)

const (
	// defaultRefreshPeersInterval represents the default polling interval
	// at which we attempt to refresh the set of known peers.
	defaultRefreshPeersInterval = 30 * time.Second

	// defaultPeerReadyTimeout is the default amount of time we'll wait for
	// a query peer to be ready to receive incoming block requests. Peers
	// cannot respond to requests until the version exchange is completed
	// upon connection establishment.
	defaultPeerReadyTimeout = 15 * time.Second

	// requiredServices are the requires services we require any candidate
	// peers to signal such that we can retrieve pruned blocks from them.
	requiredServices = wire.SFNodeNetwork | wire.SFNodeWitness

	// prunedNodeService is the service bit signaled by pruned nodes on the
	// network.
	prunedNodeService wire.ServiceFlag = 1 << 11
)

// queryPeer represents a Bitcoin network peer that we'll query for blocks.
// The ready channel serves as a signal for us to know when we can be sending
// queries to the peer. Any messages received from the peer are sent through the
// msgsRecvd channel.
type queryPeer struct {
	*peer.Peer
	ready     chan struct{}
	msgsRecvd chan wire.Message
	quit      chan struct{}
}

// signalUponDisconnect closes the peer's quit chan to signal it has
// disconnected.
func (p *queryPeer) signalUponDisconnect(f func()) {
	go func() {
		p.WaitForDisconnect()
		close(p.quit)
		f()
	}()
}

// SubscribeRecvMsg adds a OnRead subscription to the peer. All bitcoin messages
// received from this peer will be sent on the returned channel. A closure is
// also returned, that should be called to cancel the subscription.
//
// NOTE: This method exists to satisfy the query.Peer interface.
func (p *queryPeer) SubscribeRecvMsg() (<-chan wire.Message, func()) {
	return p.msgsRecvd, func() {}
}

// OnDisconnect returns a channel that will be closed once the peer disconnects.
//
// NOTE: This method exists to satisfy the query.Peer interface.
func (p *queryPeer) OnDisconnect() <-chan struct{} {
	return p.quit
}

// PrunedBlockDispatcherConfig encompasses all of the dependencies required by
// the PrunedBlockDispatcher to carry out its duties.
type PrunedBlockDispatcherConfig struct {
	// ChainParams represents the parameters of the current active chain.
	ChainParams *chaincfg.Params

	// NumTargetPeer represents the target number of peers we should
	// maintain connections with. This exists to prevent establishing
	// connections to all of the bitcoind's peers, which would be
	// unnecessary and ineffecient.
	NumTargetPeers int

	// Dial establishes connections to Bitcoin peers. This must support
	// dialing peers running over Tor if the backend also supports it.
	Dial func(string) (net.Conn, error)

	// GetPeers retrieves the active set of peers known to the backend node.
	GetPeers func() ([]btcjson.GetPeerInfoResult, error)

	// PeerReadyTimeout is the amount of time we'll wait for a query peer to
	// be ready to receive incoming block requests. Peers cannot respond to
	// requests until the version exchange is completed upon connection
	// establishment.
	PeerReadyTimeout time.Duration

	// RefreshPeersTicker is the polling ticker that signals us when we
	// should attempt to refresh the set of known peers.
	RefreshPeersTicker ticker.Ticker

	// AllowSelfPeerConns is only used to allow the tests to bypass the peer
	// self connection detecting and disconnect logic since they
	// intentionally do so for testing purposes.
	AllowSelfPeerConns bool

	// MaxRequestInvs dictates how many invs we should fit in a single
	// getdata request to a peer. This only exists to facilitate the testing
	// of a request spanning multiple getdata messages.
	MaxRequestInvs int
}

// PrunedBlockDispatcher enables a chain client to request blocks that the
// server has already pruned. This is done by connecting to the server's full
// node peers and querying them directly. Ideally, this is a capability
// supported by the server, though this is not yet possible with bitcoind.
type PrunedBlockDispatcher struct {
	cfg PrunedBlockDispatcherConfig

	// workManager handles satisfying all of our incoming pruned block
	// requests.
	workManager *query.WorkManager

	// blocksQueried represents the set of pruned blocks we've been
	// requested to query. Each block maps to a list of clients waiting to
	// be notified once the block is received.
	//
	// NOTE: The blockMtx lock must always be held when accessing this
	// field.
	blocksQueried map[chainhash.Hash][]chan *wire.MsgBlock
	blockMtx      sync.Mutex

	// currentPeers represents the set of peers we're currently connected
	// to. Each peer found here will have a worker spawned within the
	// workManager to handle our queries.
	//
	// NOTE: The peerMtx lock must always be held when accessing this
	// field.
	currentPeers map[string]*peer.Peer

	// bannedPeers represents the set of peers who have sent us an invalid
	// reply corresponding to a query. Peers within this set should not be
	// dialed.
	//
	// NOTE: The peerMtx lock must always be held when accessing this
	// field.
	bannedPeers map[string]struct{}
	peerMtx     sync.Mutex

	// peersConnected is the channel through which we'll send new peers
	// we've established connections to.
	peersConnected chan query.Peer

	// timeSource provides a mechanism to add several time samples which are
	// used to determine a median time which is then used as an offset to
	// the local clock when validating blocks received from peers.
	timeSource blockchain.MedianTimeSource

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewPrunedBlockDispatcher initializes a new PrunedBlockDispatcher instance
// backed by the given config.
func NewPrunedBlockDispatcher(cfg *PrunedBlockDispatcherConfig) (
	*PrunedBlockDispatcher, error) {

	if cfg.NumTargetPeers < 1 {
		return nil, errors.New("config option NumTargetPeer must be >= 1")
	}
	if cfg.MaxRequestInvs > wire.MaxInvPerMsg {
		return nil, fmt.Errorf("config option MaxRequestInvs must be "+
			"<= %v", wire.MaxInvPerMsg)
	}

	peersConnected := make(chan query.Peer)
	return &PrunedBlockDispatcher{
		cfg: *cfg,
		workManager: query.New(&query.Config{
			ConnectedPeers: func() (<-chan query.Peer, func(), error) {
				return peersConnected, func() {}, nil
			},
			NewWorker: query.NewWorker,
			Ranking:   query.NewPeerRanking(),
		}),
		blocksQueried:  make(map[chainhash.Hash][]chan *wire.MsgBlock),
		currentPeers:   make(map[string]*peer.Peer),
		bannedPeers:    make(map[string]struct{}),
		peersConnected: peersConnected,
		timeSource:     blockchain.NewMedianTime(),
		quit:           make(chan struct{}),
	}, nil
}

// Start allows the PrunedBlockDispatcher to begin handling incoming block
// requests.
func (d *PrunedBlockDispatcher) Start() error {
	log.Tracef("Starting pruned block dispatcher")

	if err := d.workManager.Start(); err != nil {
		return err
	}

	d.wg.Add(1)
	go d.pollPeers()

	return nil
}

// Stop stops the PrunedBlockDispatcher from accepting any more incoming block
// requests.
func (d *PrunedBlockDispatcher) Stop() {
	log.Tracef("Stopping pruned block dispatcher")

	close(d.quit)
	d.wg.Wait()

	_ = d.workManager.Stop()
}

// pollPeers continuously polls the backend node for new peers to establish
// connections to.
func (d *PrunedBlockDispatcher) pollPeers() {
	defer d.wg.Done()

	if err := d.connectToPeers(); err != nil {
		log.Warnf("Unable to establish peer connections: %v", err)
	}

	d.cfg.RefreshPeersTicker.Resume()
	defer d.cfg.RefreshPeersTicker.Stop()

	for {
		select {
		case <-d.cfg.RefreshPeersTicker.Ticks():
			// Quickly determine if we need any more peer
			// connections. If we don't, we'll wait for our next
			// tick.
			d.peerMtx.Lock()
			peersNeeded := d.cfg.NumTargetPeers - len(d.currentPeers)
			d.peerMtx.Unlock()
			if peersNeeded <= 0 {
				continue
			}

			// If we do, attempt to establish connections until
			// we've reached our target number.
			if err := d.connectToPeers(); err != nil {
				log.Warnf("Unable to establish peer "+
					"connections: %v", err)
				continue
			}

		case <-d.quit:
			return
		}
	}
}

// connectToPeers attempts to establish new peer connections until the target
// number is reached. Once a connection is successfully established, the peer is
// sent through the peersConnected channel to notify the internal workManager.
func (d *PrunedBlockDispatcher) connectToPeers() error {
	// Refresh the list of peers our backend is currently connected to, and
	// filter out any that do not meet our requirements.
	peers, err := d.cfg.GetPeers()
	if err != nil {
		return err
	}
	peers, err = filterPeers(peers)
	if err != nil {
		return err
	}
	rand.Shuffle(len(peers), func(i, j int) {
		peers[i], peers[j] = peers[j], peers[i]
	})

	// For each unbanned peer we don't already have a connection to, try to
	// establish one, and if successful, notify the peer.
	for _, peer := range peers {
		d.peerMtx.Lock()
		_, isBanned := d.bannedPeers[peer.Addr]
		_, isConnected := d.currentPeers[peer.Addr]
		d.peerMtx.Unlock()
		if isBanned || isConnected {
			continue
		}

		queryPeer, err := d.newQueryPeer(peer)
		if err != nil {
			return fmt.Errorf("unable to configure query peer %v: "+
				"%v", peer.Addr, err)
		}
		if err := d.connectToPeer(queryPeer); err != nil {
			log.Debugf("Failed connecting to peer %v: %v",
				peer.Addr, err)
			continue
		}

		select {
		case d.peersConnected <- queryPeer:
		case <-d.quit:
			return errors.New("shutting down")
		}

		// If the new peer helped us reach our target number, we're done
		// and can exit.
		d.peerMtx.Lock()
		d.currentPeers[queryPeer.Addr()] = queryPeer.Peer
		numPeers := len(d.currentPeers)
		d.peerMtx.Unlock()
		if numPeers == d.cfg.NumTargetPeers {
			break
		}
	}

	return nil
}

// filterPeers filters out any peers which cannot handle arbitrary witness block
// requests, i.e., any peer which is not considered a segwit-enabled
// "full-node".
func filterPeers(peers []btcjson.GetPeerInfoResult) (
	[]btcjson.GetPeerInfoResult, error) {

	var eligible []btcjson.GetPeerInfoResult
	for _, peer := range peers {
		rawServices, err := hex.DecodeString(peer.Services)
		if err != nil {
			return nil, err
		}
		services := wire.ServiceFlag(binary.BigEndian.Uint64(rawServices))

		// Skip nodes that cannot serve full block witness data.
		if services&requiredServices != requiredServices {
			continue
		}
		// Skip pruned nodes.
		if services&prunedNodeService == prunedNodeService {
			continue
		}

		eligible = append(eligible, peer)
	}

	return eligible, nil
}

// newQueryPeer creates a new peer instance configured to relay any received
// messages to the internal workManager.
func (d *PrunedBlockDispatcher) newQueryPeer(
	peerInfo btcjson.GetPeerInfoResult) (*queryPeer, error) {

	ready := make(chan struct{})
	msgsRecvd := make(chan wire.Message)

	cfg := &peer.Config{
		ChainParams: d.cfg.ChainParams,
		// We're not interested in transactions, so disable their relay.
		DisableRelayTx: true,
		Listeners: peer.MessageListeners{
			// Add the remote peer time as a sample for creating an
			// offset against the local clock to keep the network
			// time in sync.
			OnVersion: func(p *peer.Peer, msg *wire.MsgVersion) *wire.MsgReject {
				d.timeSource.AddTimeSample(p.Addr(), msg.Timestamp)
				return nil
			},
			// Register a callback to signal us when we can start
			// querying the peer for blocks.
			OnVerAck: func(*peer.Peer, *wire.MsgVerAck) {
				close(ready)
			},
			// Register a callback to signal us whenever the peer
			// has sent us a block message.
			OnRead: func(p *peer.Peer, _ int, msg wire.Message, err error) {
				if err != nil {
					return
				}

				var block *wire.MsgBlock
				switch msg := msg.(type) {
				case *wire.MsgBlock:
					block = msg
				case *wire.MsgVersion, *wire.MsgVerAck:
					return
				default:
					log.Debugf("Received unexpected message "+
						"%T from peer %v", msg, p.Addr())
					return
				}

				select {
				case msgsRecvd <- block:
				case <-d.quit:
				}
			},
		},
		AllowSelfConns: true,
	}
	p, err := peer.NewOutboundPeer(cfg, peerInfo.Addr)
	if err != nil {
		return nil, err
	}

	return &queryPeer{
		Peer:      p,
		ready:     ready,
		msgsRecvd: msgsRecvd,
		quit:      make(chan struct{}),
	}, nil
}

// connectToPeer attempts to establish a connection to the given peer and waits
// up to PeerReadyTimeout for the version exchange to complete so that we can
// begin sending it our queries.
func (d *PrunedBlockDispatcher) connectToPeer(peer *queryPeer) error {
	conn, err := d.cfg.Dial(peer.Addr())
	if err != nil {
		return err
	}
	peer.AssociateConnection(conn)

	select {
	case <-peer.ready:
	case <-time.After(d.cfg.PeerReadyTimeout):
		peer.Disconnect()
		return errors.New("timed out waiting for protocol negotiation")
	case <-d.quit:
		return errors.New("shutting down")
	}

	// Remove the peer once it has disconnected.
	peer.signalUponDisconnect(func() {
		d.peerMtx.Lock()
		delete(d.currentPeers, peer.Addr())
		d.peerMtx.Unlock()
	})

	return nil
}

// banPeer bans a peer by disconnecting them and ensuring we don't reconnect.
func (d *PrunedBlockDispatcher) banPeer(peer string) {
	d.peerMtx.Lock()
	defer d.peerMtx.Unlock()

	d.bannedPeers[peer] = struct{}{}
	if p, ok := d.currentPeers[peer]; ok {
		p.Disconnect()
	}
}

// Query submits a request to query the information of the given blocks.
func (d *PrunedBlockDispatcher) Query(blocks []*chainhash.Hash,
	opts ...query.QueryOption) (<-chan *wire.MsgBlock, <-chan error) {

	reqs, blockChan, err := d.newRequest(blocks)
	if err != nil {
		errChan := make(chan error, 1)
		errChan <- err
		return nil, errChan
	}

	var errChan chan error
	if len(reqs) > 0 {
		errChan = d.workManager.Query(reqs, opts...)
	}
	return blockChan, errChan
}

// newRequest construct a new query request for the given blocks to submit to
// the internal workManager. A channel is also returned through which the
// requested blocks are sent through.
func (d *PrunedBlockDispatcher) newRequest(blocks []*chainhash.Hash) (
	[]*query.Request, <-chan *wire.MsgBlock, error) {

	// Make sure the channel is buffered enough to handle all blocks.
	blockChan := make(chan *wire.MsgBlock, len(blocks))

	d.blockMtx.Lock()
	defer d.blockMtx.Unlock()

	// Each GetData message can only include up to MaxRequestInvs invs,
	// and each block consumes a single inv.
	var (
		reqs    []*query.Request
		getData *wire.MsgGetData
	)
	for i, block := range blocks {
		if getData == nil {
			getData = wire.NewMsgGetData()
		}

		if _, ok := d.blocksQueried[*block]; !ok {
			log.Debugf("Queuing new block %v for request", *block)
			inv := wire.NewInvVect(wire.InvTypeBlock, block)
			if err := getData.AddInvVect(inv); err != nil {
				return nil, nil, err
			}
		} else {
			log.Debugf("Received new request for pending query of "+
				"block %v", *block)
		}

		d.blocksQueried[*block] = append(
			d.blocksQueried[*block], blockChan,
		)

		// If we have any invs to request, or we've reached the maximum
		// allowed, queue the getdata message as is, and proceed to the
		// next if any.
		if (len(getData.InvList) > 0 && i == len(blocks)-1) ||
			len(getData.InvList) == d.cfg.MaxRequestInvs {

			reqs = append(reqs, &query.Request{
				Req:        getData,
				HandleResp: d.handleResp,
			})
			getData = nil
		}
	}

	return reqs, blockChan, nil
}

// handleResp is a response handler that will be called for every message
// received from the peer that the request was made to. It should validate the
// response against the request made, and return a Progress indicating whether
// the request was answered by this particular response.
//
// NOTE: Since the worker's job queue will be stalled while this method is
// running, it should not be doing any expensive operations. It should validate
// the response and immediately return the progress. The response should be
// handed off to another goroutine for processing.
func (d *PrunedBlockDispatcher) handleResp(req, resp wire.Message,
	peer string) query.Progress {

	// We only expect MsgBlock as replies.
	block, ok := resp.(*wire.MsgBlock)
	if !ok {
		return query.Progress{
			Progressed: false,
			Finished:   false,
		}
	}

	// We only serve MsgGetData requests.
	getData, ok := req.(*wire.MsgGetData)
	if !ok {
		return query.Progress{
			Progressed: false,
			Finished:   false,
		}
	}

	// Check that we've actually queried for this block and validate it.
	blockHash := block.BlockHash()
	d.blockMtx.Lock()
	blockChans, ok := d.blocksQueried[blockHash]
	if !ok {
		d.blockMtx.Unlock()
		return query.Progress{
			Progressed: false,
			Finished:   false,
		}
	}

	err := blockchain.CheckBlockSanity(
		btcutil.NewBlock(block), d.cfg.ChainParams.PowLimit,
		d.timeSource,
	)
	if err != nil {
		d.blockMtx.Unlock()

		log.Warnf("Received invalid block %v from peer %v: %v",
			blockHash, peer, err)
		d.banPeer(peer)

		return query.Progress{
			Progressed: false,
			Finished:   false,
		}
	}

	// Once validated, we can safely remove it.
	delete(d.blocksQueried, blockHash)

	// Check whether we have any other pending blocks we've yet to receive.
	// If we do, we'll mark the response as progressing our query, but not
	// completing it yet.
	progress := query.Progress{Progressed: true, Finished: true}
	for _, inv := range getData.InvList {
		if _, ok := d.blocksQueried[inv.Hash]; ok {
			progress.Finished = false
			break
		}
	}
	d.blockMtx.Unlock()

	// Launch a goroutine to notify all clients of the block as we don't
	// want to potentially block our workManager.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		for _, blockChan := range blockChans {
			select {
			case blockChan <- block:
			case <-d.quit:
				return
			}
		}
	}()

	return progress
}
