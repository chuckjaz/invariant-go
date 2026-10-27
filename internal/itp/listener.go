package itp

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// ListenOptions configures an ITP or IP listener.
type ListenOptions struct {
	// ListenIP if true binds a host TCP IP port instead of using Tailcat.
	ListenIP bool

	// Host is the host IP to bind when ListenIP is true (e.g. "127.0.0.1" or "").
	Host string

	// Port is the TCP port to bind when ListenIP is true (0 for dynamic/random port).
	Port int

	// PrivateKey is an explicit private key to use. If unset, KeyFile or KeysDir is used.
	PrivateKey *tailcat.PrivateKey

	// KeyFile is an explicit path to persist the WireGuard private key.
	KeyFile string

	// KeysDir is the directory to store the private key if KeyFile is unset.
	KeysDir string

	// ServiceID is used to derive the key file name if KeyFile is unset: <KeysDir>/<ServiceID>.key.
	ServiceID string

	// DERPMapURL specifies an alternate URL for the Tailcat DERP relay map.
	DERPMapURL string

	// DERPRegion specifies an explicit DERP region (useful for hermetic unit testing).
	DERPRegion *tailcfg.DERPRegion

	// AllowedClients restricts which client node keys may connect.
	AllowedClients []key.NodePublic

	// ControlPlane is the discovery control plane used to sync authorized client WireGuard keys.
	ControlPlane discovery.ControlPlane

	// SyncInterval is how often to poll ControlPlane for authorized keys (defaults to 30 seconds).
	SyncInterval time.Duration
}

// Listener wraps a net.Listener and provides ITP metadata.
type Listener struct {
	net.Listener
	server           *tailcat.Server
	IsTailcat        bool
	TailcatAddr      tailcat.Addr
	ITPAddr          string
	PublicKey        key.NodePublic
	allowedMu        sync.RWMutex
	allowedSet       map[key.NodePublic]bool
	staticAllowed    []key.NodePublic
	enforceAllowlist bool
	stopSync         chan struct{}
	closeOnce        sync.Once
	closeErr         error
}

// Port returns the TCP port if listening on IP, or 0 if listening on ITP.
func (l *Listener) Port() int {
	if l == nil || l.Listener == nil {
		return 0
	}
	if ta, ok := l.Listener.Addr().(*net.TCPAddr); ok {
		return ta.Port
	}
	return 0
}

// Listen creates a new network listener according to opts.
// By default (ListenIP == false), it opens no host IP ports and listens solely on Tailcat.
func Listen(ctx context.Context, opts ListenOptions) (*Listener, error) {
	if opts.ListenIP {
		addr := fmt.Sprintf("%s:%d", opts.Host, opts.Port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("failed to listen on TCP %s: %w", addr, err)
		}
		return &Listener{
			Listener:  ln,
			IsTailcat: false,
		}, nil
	}

	pk := opts.PrivateKey
	if pk == nil {
		keyPath := opts.KeyFile
		if keyPath == "" && opts.KeysDir != "" && opts.ServiceID != "" {
			keyPath = filepath.Join(opts.KeysDir, opts.ServiceID+".key")
		}

		var err error
		pk, err = LoadOrGenerateKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load or generate key: %w", err)
		}
	}

	var allowed []key.NodePublic
	allowedSet := make(map[key.NodePublic]bool)
	enforceAllowlist := len(opts.AllowedClients) > 0 || opts.ControlPlane != nil

	if len(opts.AllowedClients) > 0 {
		allowed = append(allowed, opts.AllowedClients...)
		for _, k := range opts.AllowedClients {
			allowedSet[k] = true
		}
	}

	// Fetch authorized keys from ControlPlane if available
	if opts.ControlPlane != nil {
		// Seed with own public key so tailcat.Server activates allowlist mode immediately
		selfPub := pk.Private.Public()
		allowed = append(allowed, selfPub)
		allowedSet[selfPub] = true

		if authKeys, err := opts.ControlPlane.AuthorizedKeys(ctx); err == nil {
			for _, kStr := range authKeys {
				var k key.NodePublic
				if err := k.UnmarshalText([]byte(kStr)); err == nil {
					allowed = append(allowed, k)
					allowedSet[k] = true
				}
			}
		}
	}

	logf := logger.Logf(log.Printf)
	if log.Writer() == io.Discard {
		logf = logger.Discard
	}

	srv := &tailcat.Server{
		Key:            pk.Private,
		PresharedKey:   pk.Public.PresharedKey,
		Logf:           logf,
		Region:         opts.DERPRegion,
		DERPMapURL:     opts.DERPMapURL,
		AllowedClients: allowed,
	}

	ln, err := srv.Listen(ctx, "tcp", ":80")
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("failed to listen on Tailcat: %w", err)
	}

	tcAddr := srv.TailcatAddr()

	staticCopy := make([]key.NodePublic, len(opts.AllowedClients))
	copy(staticCopy, opts.AllowedClients)

	l := &Listener{
		Listener:         ln,
		server:           srv,
		IsTailcat:        true,
		TailcatAddr:      tcAddr,
		ITPAddr:          fmt.Sprintf("itp://%s", tcAddr),
		PublicKey:        pk.Private.Public(),
		allowedSet:       allowedSet,
		staticAllowed:    staticCopy,
		enforceAllowlist: enforceAllowlist,
		stopSync:         make(chan struct{}),
	}

	// If ControlPlane is provided, periodically refresh authorized keys
	if opts.ControlPlane != nil {
		interval := opts.SyncInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		go l.syncLoop(opts.ControlPlane, interval)
	}

	return l, nil
}

// Accept waits for and returns the next connection to the listener,
// rejecting connections from clients that have been evicted from the allowlist.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.IsTailcat && l.enforceAllowlist {
			pk, ok := l.PeerKey(conn.RemoteAddr().String())
			if !ok || !l.IsClientAllowed(pk) {
				_ = conn.Close()
				continue
			}
		}
		return conn, nil
	}
}

func (l *Listener) syncLoop(cp discovery.ControlPlane, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopSync:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			keys, err := cp.AuthorizedKeys(ctx)
			cancel()
			if err == nil {
				newSet := make(map[key.NodePublic]bool)
				newSet[l.PublicKey] = true
				for _, sk := range l.staticAllowed {
					newSet[sk] = true
				}
				for _, kStr := range keys {
					var k key.NodePublic
					if err := k.UnmarshalText([]byte(kStr)); err == nil {
						newSet[k] = true
						l.server.AddAllowedClient(k)
					}
				}
				l.allowedMu.Lock()
				l.allowedSet = newSet
				l.allowedMu.Unlock()
			}
		}
	}
}

// AddAllowedClient adds a client's public key to the server's allowlist.
func (l *Listener) AddAllowedClient(k key.NodePublic) {
	l.allowedMu.Lock()
	if l.allowedSet == nil {
		l.allowedSet = make(map[key.NodePublic]bool)
	}
	l.allowedSet[k] = true
	l.enforceAllowlist = true
	l.allowedMu.Unlock()

	if l.server != nil {
		l.server.AddAllowedClient(k)
	}
}

// RemoveAllowedClient removes a client's public key from the listener's active allowlist.
func (l *Listener) RemoveAllowedClient(k key.NodePublic) {
	l.allowedMu.Lock()
	delete(l.allowedSet, k)
	l.allowedMu.Unlock()
}

// IsClientAllowed reports whether the given WireGuard public key is currently permitted to connect.
func (l *Listener) IsClientAllowed(k key.NodePublic) bool {
	l.allowedMu.RLock()
	defer l.allowedMu.RUnlock()
	if !l.enforceAllowlist {
		return true
	}
	return l.allowedSet[k]
}

// PeerKey extracts the client's WireGuard public key from a remote TCP address string (e.g. r.RemoteAddr).
func (l *Listener) PeerKey(remoteAddr string) (key.NodePublic, bool) {
	if l.server == nil {
		return key.NodePublic{}, false
	}
	ta, err := net.ResolveTCPAddr("tcp", remoteAddr)
	if err != nil {
		return key.NodePublic{}, false
	}
	localAddr := l.Addr()
	if localAddr == nil {
		localAddr = &net.TCPAddr{}
	}
	for _, env := range l.server.PeerEnv(localAddr, ta) {
		if after, ok := strings.CutPrefix(env, "TAILCAT_PEER_KEY="); ok {
			kStr := after
			var k key.NodePublic
			if err := k.UnmarshalText([]byte(kStr)); err == nil {
				return k, true
			}
		}
	}
	return key.NodePublic{}, false
}

// Close closes the underlying listener and Tailcat server.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		if l.stopSync != nil {
			close(l.stopSync)
		}
		var errs []error
		if err := l.Listener.Close(); err != nil {
			errs = append(errs, err)
		}
		if l.server != nil {
			if err := l.server.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			l.closeErr = errs[0]
		}
	})
	return l.closeErr
}
