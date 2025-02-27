package node

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/libp2p/go-libp2p"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	dual "github.com/libp2p/go-libp2p-kad-dht/dual"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/dominant-strategies/go-quai/cmd/utils"
	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/log"
	"github.com/dominant-strategies/go-quai/p2p/node/peerManager"
	"github.com/dominant-strategies/go-quai/p2p/node/pubsubManager"
	"github.com/dominant-strategies/go-quai/p2p/node/requestManager"
	"github.com/dominant-strategies/go-quai/p2p/node/streamManager"
	"github.com/dominant-strategies/go-quai/p2p/protocol"
	"github.com/dominant-strategies/go-quai/quai"
)

const (
	// c_defaultCacheSize is the default size for the p2p cache
	c_defaultCacheSize = 32
)

// P2PNode represents a libp2p node
type P2PNode struct {
	// Backend for handling consensus data
	consensus quai.ConsensusAPI

	// Gossipsub instance
	pubsub *pubsubManager.PubsubManager

	// Peer management interface instance
	peerManager peerManager.PeerManager

	// Request management interface instance
	requestManager requestManager.RequestManager

	// Caches for each type of data we may receive
	cache map[string]map[reflect.Type]*lru.Cache[common.Hash, interface{}]

	// Channel to signal when to quit and shutdown
	quitCh chan struct{}

	// runtime context
	ctx context.Context

	// used to control all the different sub processes of the P2PNode
	cancel context.CancelFunc

	// host management interface
	host host.Host

	// dht interface
	dht *dual.DHT
}

// Returns a new libp2p node.
// The node is created with the given context and options passed as arguments.
func NewNode(ctx context.Context, quitCh chan struct{}) (*P2PNode, error) {
	ipAddr := viper.GetString(utils.IPAddrFlag.Name)
	port := viper.GetString(utils.P2PPortFlag.Name)

	// Peer manager handles both connection management and connection gating
	peerMgr, err := peerManager.NewManager(
		ctx,
		viper.GetInt(utils.MaxPeersFlag.Name), // LowWater
		2*viper.GetInt(utils.MaxPeersFlag.Name), // HighWater
		nil,
	)
	if err != nil {
		log.Global.Fatalf("error creating libp2p connection manager: %s", err)
		return nil, err
	}

	// Create the libp2p host
	var dht *dual.DHT
	host, err := libp2p.New(
		// use a private key for persistent identity
		libp2p.Identity(getNodeKey()),

		// pass the ip address and port to listen on
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/%s/tcp/%s", ipAddr, port),
		),

		// support all transports
		libp2p.DefaultTransports,

		// support Noise connections
		libp2p.Security(noise.ID, noise.New),

		// Optionally attempt to configure network port mapping with UPnP
		func() libp2p.Option {
			if viper.GetBool(utils.PortMapFlag.Name) {
				return libp2p.NATPortMap()
			} else {
				return nil
			}
		}(),

		// Enable NAT detection service
		libp2p.EnableNATService(),

		// If publicly reachable, provide a relay service for other peers
		libp2p.EnableRelayService(),

		// If behind NAT, automatically advertise relay address through relay peers
		// TODO: today the bootnodes act as static relays. In the future we should dynamically select relays from publicly reachable peers.
		libp2p.EnableAutoRelayWithStaticRelays(peerMgr.RefreshBootpeers()),

		// Attempt to open a direct connection with relayed peers, using relay
		// nodes to coordinate the holepunch.
		libp2p.EnableHolePunching(),

		// Connection manager will tag and prioritize peers
		libp2p.ConnectionManager(peerMgr),

		// Connection gater will prevent connections to blacklisted peers
		libp2p.ConnectionGater(peerMgr),

		// Let this host use the DHT to find other hosts
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			dht, err = dual.New(ctx, h,
				dual.WanDHTOption(
					kaddht.Mode(kaddht.ModeServer),
					kaddht.BootstrapPeersFunc(func() []peer.AddrInfo {
						return peerMgr.RefreshBootpeers()
					}),
					kaddht.ProtocolPrefix("/quai"),
					kaddht.RoutingTableRefreshPeriod(1*time.Minute),
				),
			)
			return dht, err
		}),
	)
	if err != nil {
		log.Global.Fatalf("error creating libp2p host: %s", err)
		return nil, err
	}

	idOpts := []identify.Option{
		// TODO: Add version number + commit hash
		identify.UserAgent("go-quai"),
		identify.ProtocolVersion(string(protocol.ProtocolVersion)),
	}

	// Create the identity service
	idServ, err := identify.NewIDService(host, idOpts...)
	if err != nil {
		log.Global.Fatalf("error creating libp2p identity service: %s", err)
		return nil, err
	}
	// Register the identity service with the host
	idServ.Start()

	// log the p2p node's ID
	nodeID := host.ID()
	log.Global.Infof("node created: %s", nodeID)

	// Set peer manager's self ID
	peerMgr.SetSelfID(nodeID)

	// Set the DHT for the peer manager
	peerMgr.SetDHT(dht)

	// Create a gossipsub instance with helper functions
	ps, err := pubsubManager.NewGossipSubManager(ctx, host)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	p2p := &P2PNode{
		ctx:            ctx,
		pubsub:         ps,
		peerManager:    peerMgr,
		requestManager: requestManager.NewManager(),
		cache:          initializeCaches(common.GenerateLocations(common.MaxRegions, common.MaxZones)),
		quitCh:         quitCh,
		cancel:         cancel,
		host:           host,
		dht:            dht,
	}

	sm, err := streamManager.NewStreamManager(p2p, host)
	if err != nil {
		return nil, err
	}
	sm.Start()

	p2p.peerManager.SetStreamManager(sm)

	return p2p, nil
}

// Close performs cleanup of resources used by P2PNode
func (p *P2PNode) Close() error {
	p.cancel()
	// Close PubSub manager
	if err := p.pubsub.Stop(); err != nil {
		log.Global.Errorf("error closing pubsub manager: %s", err)
	}

	// Close the stream manager
	if err := p.peerManager.Stop(); err != nil {
		log.Global.Errorf("error closing peer manager: %s", err)
	}

	// Close DHT
	if err := p.dht.Close(); err != nil {
		log.Global.Errorf("error closing DHT: %s", err)
	}

	// Close the libp2p host
	if err := p.host.Close(); err != nil {
		log.Global.Errorf("error closing libp2p host: %s", err)
	}

	close(p.quitCh)
	return nil
}

// acceptableTypes is used to filter out unsupported broadcast types
var acceptableTypes = map[reflect.Type]struct{}{
	reflect.TypeOf(types.WorkObjectHeader{}):     {},
	reflect.TypeOf(types.WorkObjectBlockView{}):  {},
	reflect.TypeOf(types.WorkObjectHeaderView{}): {},
	reflect.TypeOf(types.Transactions{}):         {},
}

func initializeCaches(locations []common.Location) map[string]map[reflect.Type]*lru.Cache[common.Hash, interface{}] {
	caches := make(map[string]map[reflect.Type]*lru.Cache[common.Hash, interface{}])
	for _, location := range locations {
		locCache := map[reflect.Type]*lru.Cache[common.Hash, interface{}]{}
		for typ := range acceptableTypes {
			locCache[reflect.PointerTo(typ)] = createCache(c_defaultCacheSize)
		}
		caches[location.Name()] = locCache
	}
	return caches
}

func createCache(size int) *lru.Cache[common.Hash, interface{}] {
	cache, err := lru.New[common.Hash, interface{}](size)
	if err != nil {
		log.Global.Fatal("error initializing cache;", err)
	}
	return cache
}

// Get the full multi-address to reach our node
func (p *P2PNode) p2pAddress() (multiaddr.Multiaddr, error) {
	return multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s", p.peerManager.GetSelfID()))
}

// Helper to access the corresponding data cache
func (p *P2PNode) pickCache(datatype interface{}, location common.Location) *lru.Cache[common.Hash, interface{}] {
	return p.cache[location.Name()][reflect.TypeOf(datatype)]
}

// Add a datagram into the corresponding cache
func (p *P2PNode) cacheAdd(hash common.Hash, data interface{}, location common.Location) {
	cache := p.pickCache(data, location)
	cache.Add(hash, data)
}

// Get a datagram from the corresponding cache
func (p *P2PNode) cacheGet(hash common.Hash, datatype interface{}, location common.Location) (interface{}, bool) {
	cache := p.pickCache(datatype, location)
	return cache.Get(hash)
}
