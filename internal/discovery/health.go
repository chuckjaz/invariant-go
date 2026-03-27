package discovery

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"invariant/internal/identity"
)

type HealthTracker struct {
	mu       sync.RWMutex
	statuses map[string]*healthStatus
	interval time.Duration
	timeout  time.Duration
	listAll  func() []ServiceRegistration
	remove   func(string)
	stopCh   chan struct{}
}

type healthStatus struct {
	healthy      bool
	firstFailure time.Time
	lastCheck    time.Time
}

func NewHealthTracker(interval, timeout time.Duration, listAll func() []ServiceRegistration, remove func(string)) *HealthTracker {
	t := &HealthTracker{
		statuses: make(map[string]*healthStatus),
		interval: interval,
		timeout:  timeout,
		listAll:  listAll,
		remove:   remove,
		stopCh:   make(chan struct{}),
	}
	if interval > 0 {
		go t.loop()
	}
	return t
}

func (t *HealthTracker) Close() {
	if t.interval > 0 {
		close(t.stopCh)
	}
}

func (t *HealthTracker) MarkHealthy(id string) {
	if t.interval == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.statuses[id] = &healthStatus{
		healthy:   true,
		lastCheck: time.Now(),
	}
}

func (t *HealthTracker) Sort(descs []ServiceDescription) {
	if t.interval == 0 || len(descs) <= 1 {
		return
	}
	t.mu.RLock()
	healthMap := make(map[string]bool, len(descs))
	for _, d := range descs {
		if st, ok := t.statuses[d.ID]; ok {
			healthMap[d.ID] = st.healthy
		} else {
			// Considered healthy if just registered but not yet captured by loop.
			healthMap[d.ID] = true
		}
	}
	t.mu.RUnlock()

	sort.SliceStable(descs, func(i, j int) bool {
		h1 := healthMap[descs[i].ID]
		h2 := healthMap[descs[j].ID]
		return h1 && !h2
	})
}

func (t *HealthTracker) loop() {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.checkAll()
		}
	}
}

func (t *HealthTracker) checkAll() {
	services := t.listAll()
	now := time.Now()

	var wg sync.WaitGroup
	for _, s := range services {
		t.mu.RLock()
		status := t.statuses[s.ID]
		t.mu.RUnlock()

		if status != nil && now.Sub(status.lastCheck) < t.interval {
			continue // Checked/Registered very recently
		}

		wg.Add(1)
		go func(reg ServiceRegistration) {
			defer wg.Done()

			client := identity.NewClient(reg.Address, &http.Client{Timeout: 2 * time.Second})
			actualID := client.ID()
			isHealthy := (actualID == reg.ID)
			checkTime := time.Now()

			t.mu.Lock()
			st, ok := t.statuses[reg.ID]
			if !ok {
				st = &healthStatus{}
				t.statuses[reg.ID] = st
			}

			// Do not override if concurrently updated just now
			if checkTime.Sub(st.lastCheck) < t.interval && ok {
				t.mu.Unlock()
				return
			}

			st.lastCheck = checkTime

			if isHealthy {
				st.healthy = true
				st.firstFailure = time.Time{}
			} else {
				if st.healthy || st.firstFailure.IsZero() {
					st.firstFailure = st.lastCheck
				}
				st.healthy = false

				if t.timeout > 0 && st.lastCheck.Sub(st.firstFailure) > t.timeout {
					t.remove(reg.ID)
					delete(t.statuses, reg.ID)
				}
			}
			t.mu.Unlock()
		}(s)
	}
	wg.Wait()
}
