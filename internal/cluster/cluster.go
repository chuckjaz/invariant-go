package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/names"
	"invariant/internal/notify"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// serviceConfig persists configuration parameters to allow restarting services.
type serviceConfig struct {
	serviceType      string
	discoveryURL     string
	distributeURL    string
	repFactor        int
	maxAttempts      int
	additionalNotify []string
	slotsURL         string
	finderURL        string
	btreeSlotID      string
	journalSlotID    string
}

// Machine represents a simulated server running zero or more services.
type Machine struct {
	id          string
	cluster     *Cluster
	dataDir     string
	mu          sync.Mutex
	servers     map[string]*http.Server
	listeners   map[string]net.Listener
	ports       map[string]int
	cancels     map[string]context.CancelFunc
	configs     map[string]*serviceConfig
	closers     map[string]io.Closer
	storageID   string
	storageNode storage.Storage
	finderNode  finder.Finder
}

// HandlerRegistry defines an interface for registering HTTP handlers dynamically.
type HandlerRegistry interface {
	RegisterHandler(host string, h http.Handler)
}

// Cluster manages a set of simulated machines and services.
type Cluster struct {
	mu                 sync.Mutex
	tempDir            string
	machines           map[string]*Machine
	UseInMemoryStorage bool
	Registry           HandlerRegistry
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

func (m *Machine) registerHandler(serviceType string, port int, handler http.Handler) {
	if m.cluster.Registry != nil {
		host := fmt.Sprintf("127.0.0.1:%d", port)
		m.cluster.Registry.RegisterHandler(host, handler)
	}
}

// StorageID returns the storage ID of the machine if a storage service has been started.
func (m *Machine) StorageID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storageID
}

// StorageNode returns the active storage node instance if running.
func (m *Machine) StorageNode() storage.Storage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storageNode
}

// FinderNode returns the active finder node instance if running.
func (m *Machine) FinderNode() finder.Finder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finderNode
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
	m.registerHandler("discovery", port, server)

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
	m.registerHandler("names", port, server)

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
	m.registerHandler("slots", port, server)

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
	m.registerHandler("distribute", port, server)

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
			var mu sync.Mutex
			running := false
			for {
				select {
				case <-ticker.C:
					mu.Lock()
					if running {
						mu.Unlock()
						continue
					}
					running = true
					mu.Unlock()

					d.Sync()

					mu.Lock()
					running = false
					mu.Unlock()
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
func (m *Machine) StartStorage(ctx context.Context, discoveryURL string, distributeURL string, additionalNotifyURLs ...string) (string, error) {
	port, err := m.allocatePort("storage")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["storage"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	var s storage.Storage
	if m.cluster.UseInMemoryStorage {
		s = storage.NewInMemoryStorage()
	} else {
		dir := filepath.Join(m.dataDir, "storage")
		s = storage.NewFileSystemStorage(dir)
	}
	m.storageID = s.(interface{ ID() string }).ID()
	m.storageNode = s

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
		serviceType:      "storage",
		discoveryURL:     discoveryURL,
		distributeURL:    distributeURL,
		additionalNotify: additionalNotifyURLs,
	}
	m.registerHandler("storage", port, sServer)

	if discoveryURL != "" {
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, m.storageID, "http://127.0.0.1", port, []string{"storage-v1", "batch-storage-v1"})
		if err != nil {
			cancel()
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
	}

	var notifyClients []storage.NotifyClient
	if distributeURL != "" {
		distClient := distribute.NewClient(distributeURL, nil)
		if err := distClient.Register(m.storageID); err != nil {
			cancel()
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with distribute: %w", err)
		}
		notifyClients = append(notifyClients, notify.NewClient(distributeURL, nil))
	}

	for _, u := range additionalNotifyURLs {
		if u != "" {
			notifyClients = append(notifyClients, notify.NewClient(u, nil))
		}
	}

	if len(notifyClients) > 0 {
		// Use batchSize 10000 and 10ms for fast notifications in scale tests.
		sServer.StartNotification(srvCtx, notifyClients, 10000, 10*time.Millisecond)
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartFinder boots a finder service on the machine, optionally registering it.
func (m *Machine) StartFinder(ctx context.Context, discoveryURL string) (string, error) {
	port, err := m.allocatePort("finder")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["finder"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	// Generate a 32-byte hex string node ID for finder
	idBytes := make([]byte, 32)
	_, _ = rand.Read(idBytes)
	idStr := hex.EncodeToString(idBytes)

	f, err := finder.NewMemoryFinder(idStr)
	if err != nil {
		return "", fmt.Errorf("failed to create finder: %w", err)
	}
	m.finderNode = f

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	server := finder.NewFinderServer(f, disc)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	m.servers["finder"] = srv
	m.listeners["finder"] = l
	m.configs["finder"] = &serviceConfig{
		serviceType:  "finder",
		discoveryURL: discoveryURL,
	}
	m.registerHandler("finder", port, server)

	if discoveryURL != "" {
		dClient := discovery.NewClient(discoveryURL, nil)
		err := discovery.AdvertiseAndRegister(ctx, dClient, idStr, "http://127.0.0.1", port, []string{"finder-v1", "notify-v1"})
		if err != nil {
			_ = srv.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
	}

	go func() {
		_ = srv.Serve(l)
	}()

	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// StartKV boots a key-value service on the machine, optionally registering it.
func (m *Machine) StartKV(ctx context.Context, discoveryURL string, slotsURL string, finderURL string, btreeSlotID, journalSlotID string) (string, error) {
	port, err := m.allocatePort("kv")
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.servers["kv"]; ok {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}

	disc := discovery.NewClient(discoveryURL, nil)
	slotsClient := slots.NewClient(slotsURL, nil)
	finderClient := finder.NewClient(finderURL, nil)
	storageClient := storage.NewAggregateClient(finderClient, disc, 3, 1000)

	dir := filepath.Join(m.dataDir, "kv-journal")
	_ = os.MkdirAll(dir, 0755)

	var writerOpts content.WriterOptions // use default options

	store, err := kv.NewFileKeyValueStore(
		ctx,
		slotsClient,
		btreeSlotID,
		nil,
		journalSlotID,
		nil,
		storageClient,
		dir,
		10*1024*1024,
		1000,
		100,
		writerOpts,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create kv store: %w", err)
	}

	kvServer := kv.NewServer(store)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		store.Close()
		return "", fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: kvServer,
	}

	m.servers["kv"] = srv
	m.listeners["kv"] = l
	m.closers["kv"] = store
	m.configs["kv"] = &serviceConfig{
		serviceType:   "kv",
		discoveryURL:  discoveryURL,
		slotsURL:      slotsURL,
		finderURL:     finderURL,
		btreeSlotID:   btreeSlotID,
		journalSlotID: journalSlotID,
	}
	m.registerHandler("kv", port, kvServer)

	if discoveryURL != "" {
		myID := "kv-server-" + m.id
		err := discovery.AdvertiseAndRegister(ctx, disc, myID, "http://127.0.0.1", port, []string{"kv-v1", "kv-batch-v1"})
		if err != nil {
			_ = srv.Close()
			_ = l.Close()
			_ = store.Close()
			return "", fmt.Errorf("failed to register with discovery: %w", err)
		}
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

	if serviceType == "storage" {
		m.storageNode = nil
	} else if serviceType == "finder" {
		m.finderNode = nil
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
		return m.StartStorage(ctx, cfg.discoveryURL, cfg.distributeURL, cfg.additionalNotify...)
	case "finder":
		return m.StartFinder(ctx, cfg.discoveryURL)
	case "kv":
		return m.StartKV(ctx, cfg.discoveryURL, cfg.slotsURL, cfg.finderURL, cfg.btreeSlotID, cfg.journalSlotID)
	default:
		return "", fmt.Errorf("unknown service type %q", serviceType)
	}
}

// StopAll stops all services running on this machine.
func (m *Machine) StopAll() {
	m.StopService("kv")
	m.StopService("storage")
	m.StopService("finder")
	m.StopService("distribute")
	m.StopService("slots")
	m.StopService("names")
	m.StopService("discovery")
}
