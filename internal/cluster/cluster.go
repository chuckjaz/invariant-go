package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/names"
	"invariant/internal/notify"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// serviceConfig persists configuration parameters to allow restarting services.
type serviceConfig struct {
	serviceType   string
	discoveryURL  string
	distributeURL string
	repFactor     int
	maxAttempts   int
}

// Machine represents a simulated server running zero or more services.
type Machine struct {
	id        string
	cluster   *Cluster
	dataDir   string
	mu        sync.Mutex
	servers   map[string]*http.Server
	listeners map[string]net.Listener
	ports     map[string]int
	cancels   map[string]context.CancelFunc
	configs   map[string]*serviceConfig
	closers   map[string]io.Closer
	storageID string
}

// Cluster manages a set of simulated machines and services.
type Cluster struct {
	mu       sync.Mutex
	tempDir  string
	machines map[string]*Machine
}

// NewCluster creates a new cluster orchestrator using a temporary directory for persisted state.
func NewCluster() (*Cluster, error) {
	tmpDir, err := os.MkdirTemp("", "invariant-cluster-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster temp dir: %w", err)
	}
	return &Cluster{
		tempDir:  tmpDir,
		machines: make(map[string]*Machine),
	}, nil
}

// NewMachine adds a new machine with the given name to the cluster.
func (c *Cluster) NewMachine(name string) (*Machine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.machines[name]; exists {
		return nil, fmt.Errorf("machine %q already exists in the cluster", name)
	}

	dataDir := filepath.Join(c.tempDir, name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir for machine %s: %w", name, err)
	}

	m := &Machine{
		id:        name,
		cluster:   c,
		dataDir:   dataDir,
		servers:   make(map[string]*http.Server),
		listeners: make(map[string]net.Listener),
		ports:     make(map[string]int),
		cancels:   make(map[string]context.CancelFunc),
		configs:   make(map[string]*serviceConfig),
		closers:   make(map[string]io.Closer),
	}
	c.machines[name] = m
	return m, nil
}

// Close stops all running services and cleans up all temporary state.
func (c *Cluster) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, m := range c.machines {
		m.StopAll()
	}
	_ = os.RemoveAll(c.tempDir)
}

// ID returns the machine's configured identifier.
func (m *Machine) ID() string {
	return m.id
}

// StorageID returns the storage ID of the machine if a storage service has been started.
func (m *Machine) StorageID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storageID
}

// ServiceURL returns the HTTP URL of a running service.
func (m *Machine) ServiceURL(serviceType string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if port, exists := m.ports[serviceType]; exists {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return ""
}

// allocatePort allocates a free port for a service on this machine, reuse-bound.
func (m *Machine) allocatePort(serviceType string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if port, exists := m.ports[serviceType]; exists {
		return port, nil
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to allocate port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	m.ports[serviceType] = port
	return port, nil
}

// StartDiscovery boots a discovery service on the machine.
func (m *Machine) StartDiscovery(ctx context.Context) (string, error) {
	port, err := m.allocatePort("discovery")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["discovery"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	dir := filepath.Join(m.dataDir, "discovery")
	fsd, err := discovery.NewFileSystemDiscovery(dir, 1*time.Hour)
	if err != nil {
		return "", fmt.Errorf("failed to create discovery store: %w", err)
	}
	fsdWithHealth := fsd.WithHealthTracking(100*time.Millisecond, 500*time.Millisecond)

	server := discovery.NewDiscoveryServer(fsdWithHealth)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		_ = fsd.Close()
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	m.servers["discovery"] = srv
	m.listeners["discovery"] = l
	m.closers["discovery"] = fsd
	m.configs["discovery"] = &serviceConfig{serviceType: "discovery"}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartNames boots a names service on the machine, optionally registering it.
func (m *Machine) StartNames(ctx context.Context, discoveryURL string) (string, error) {
	port, err := m.allocatePort("names")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["names"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	dir := filepath.Join(m.dataDir, "names")
	fsn, err := names.NewFileSystemNames(dir, 1*time.Hour)
	if err != nil {
		return "", fmt.Errorf("failed to create names store: %w", err)
	}

	server := names.NewNamesServer(fsn)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		_ = fsn.Close()
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	m.servers["names"] = srv
	m.listeners["names"] = l
	m.closers["names"] = fsn
	m.configs["names"] = &serviceConfig{
		serviceType:  "names",
		discoveryURL: discoveryURL,
	}

	if discoveryURL != "" {
		id := fsn.ID()
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, id, "http://127.0.0.1", port, []string{"names-v1"})
		if err != nil {
			_ = srv.Close()
			_ = fsn.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartSlots boots a slots service on the machine, optionally registering it.
func (m *Machine) StartSlots(ctx context.Context, discoveryURL string) (string, error) {
	port, err := m.allocatePort("slots")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["slots"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	dir := filepath.Join(m.dataDir, "slots")
	fss, err := slots.NewFileSystemSlots(dir, 1*time.Hour)
	if err != nil {
		return "", fmt.Errorf("failed to create slots store: %w", err)
	}

	server := slots.NewServer(fss)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		_ = fss.Close()
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	m.servers["slots"] = srv
	m.listeners["slots"] = l
	m.closers["slots"] = fss
	m.configs["slots"] = &serviceConfig{
		serviceType:  "slots",
		discoveryURL: discoveryURL,
	}

	if discoveryURL != "" {
		id := fss.ID()
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, id, "http://127.0.0.1", port, []string{"slots-v1"})
		if err != nil {
			_ = srv.Close()
			_ = fss.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartDistribute boots a distribute service on the machine, optionally registering it.
func (m *Machine) StartDistribute(ctx context.Context, discoveryURL string, repFactor int, maxAttempts int) (string, error) {
	port, err := m.allocatePort("distribute")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["distribute"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	d := distribute.NewInMemoryDistribute(disc, repFactor, maxAttempts, "", 0)

	server := distribute.NewDistributeServer("", d)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	srvCtx, cancel := context.WithCancel(context.Background())

	m.servers["distribute"] = srv
	m.listeners["distribute"] = l
	m.cancels["distribute"] = cancel
	m.configs["distribute"] = &serviceConfig{
		serviceType:  "distribute",
		discoveryURL: discoveryURL,
		repFactor:    repFactor,
		maxAttempts:  maxAttempts,
	}

	if discoveryURL != "" {
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, server.ID(), "http://127.0.0.1", port, []string{"distribute-v1", "notify-v1"})
		if err != nil {
			cancel()
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}

		// Run custom sync loop in the background, bound to our cancel context.
		go func() {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					d.Sync()
				case <-srvCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartStorage boots a storage service on the machine, optionally registering it.
func (m *Machine) StartStorage(ctx context.Context, discoveryURL string, distributeURL string) (string, error) {
	port, err := m.allocatePort("storage")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["storage"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	dir := filepath.Join(m.dataDir, "storage")
	s := storage.NewFileSystemStorage(dir)
	m.storageID = s.ID()

	sServer := storage.NewStorageServer(s)
	if discoveryURL != "" {
		dClient := discovery.NewClient(discoveryURL, nil)
		sServer.WithDiscovery(dClient)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: sServer,
	}

	srvCtx, cancel := context.WithCancel(context.Background())

	m.servers["storage"] = srv
	m.listeners["storage"] = l
	m.cancels["storage"] = cancel
	m.configs["storage"] = &serviceConfig{
		serviceType:   "storage",
		discoveryURL:  discoveryURL,
		distributeURL: distributeURL,
	}

	if discoveryURL != "" {
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, m.storageID, "http://127.0.0.1", port, []string{"storage-v1", "batch-storage-v1"})
		if err != nil {
			cancel()
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
	}

	if distributeURL != "" {
		distClient := distribute.NewClient(distributeURL, nil)
		if err := distClient.Register(m.storageID); err != nil {
			cancel()
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with distribute: %w", err)
		}

		notifyClients := []storage.NotifyClient{notify.NewClient(distributeURL, nil)}
		sServer.StartNotification(srvCtx, notifyClients, 1, 10*time.Millisecond)
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StopService stops a specific service running on this machine.
func (m *Machine) StopService(serviceType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cancel, ok := m.cancels[serviceType]; ok {
		cancel()
		delete(m.cancels, serviceType)
	}

	if srv, ok := m.servers[serviceType]; ok {
		_ = srv.Close()
		delete(m.servers, serviceType)
	}

	if l, ok := m.listeners[serviceType]; ok {
		_ = l.Close()
		delete(m.listeners, serviceType)
	}

	if closer, ok := m.closers[serviceType]; ok {
		_ = closer.Close()
		delete(m.closers, serviceType)
	}
}

// StartService restarts a previously configured and stopped service on the machine, binding it to the same port.
func (m *Machine) StartService(ctx context.Context, serviceType string) (string, error) {
	m.mu.Lock()
	cfg, ok := m.configs[serviceType]
	m.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("service %q has not been configured/started on this machine before", serviceType)
	}

	switch serviceType {
	case "discovery":
		return m.StartDiscovery(ctx)
	case "names":
		return m.StartNames(ctx, cfg.discoveryURL)
	case "slots":
		return m.StartSlots(ctx, cfg.discoveryURL)
	case "distribute":
		return m.StartDistribute(ctx, cfg.discoveryURL, cfg.repFactor, cfg.maxAttempts)
	case "storage":
		return m.StartStorage(ctx, cfg.discoveryURL, cfg.distributeURL)
	default:
		return "", fmt.Errorf("unknown service type %q", serviceType)
	}
}

// StopAll stops all services running on this machine.
func (m *Machine) StopAll() {
	m.StopService("storage")
	m.StopService("distribute")
	m.StopService("slots")
	m.StopService("names")
	m.StopService("discovery")
}
