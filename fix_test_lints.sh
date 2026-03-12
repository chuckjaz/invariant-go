#!/bin/bash
# internal/finder/server_test.go
sed -i 's/\.Find(blockAddr)/.Find(context.Background(), blockAddr)/g' internal/finder/server_test.go
sed -i 's/\.Notify(/.Notify(context.Background(),/g' internal/finder/server_test.go
sed -i 's/\.Peer(/.Peer(context.Background(),/g' internal/finder/server_test.go
sed -i 's/\.Register(discovery\.ServiceRegistration{/.Register(context.Background(), discovery.ServiceRegistration{/g' internal/finder/server_test.go

# internal/distribute/sync_test.go and internal/finder/server_test.go
sed -i 's/func (m \*mockDiscovery) Find(protocol string/func (m *mockDiscovery) Find(ctx context.Context, protocol string/g' internal/finder/server_test.go internal/distribute/sync_test.go
sed -i 's/func (m \*mockDiscovery) Get(id string)/func (m *mockDiscovery) Get(ctx context.Context, id string)/g' internal/finder/server_test.go internal/distribute/sync_test.go
sed -i 's/func (m \*mockDiscovery) Register(reg discovery\.ServiceRegistration)/func (m *mockDiscovery) Register(ctx context.Context, reg discovery.ServiceRegistration)/g' internal/finder/server_test.go internal/distribute/sync_test.go
sed -i 's/\(store[1|2|3]\)\.Store(bytes\.NewReader(/\1.Store(context.Background(), bytes.NewReader(/g' internal/distribute/sync_test.go
sed -i 's/\(store[1|2|3]\)\.Has(addr)/\1.Has(context.Background(), addr)/g' internal/distribute/sync_test.go

# internal/slots/slots_test.go
sed -i 's/\.Get(slotID)/.Get(context.Background(), slotID)/g' internal/slots/slots_test.go
sed -i 's/\.Create(/.Create(context.Background(), /g' internal/slots/slots_test.go
sed -i 's/\.Update(/.Update(context.Background(), /g' internal/slots/slots_test.go

# Add context imports if necessary
grep -q '"context"' internal/slots/slots_test.go || sed -i '/"testing"/a \"context"' internal/slots/slots_test.go
grep -q '"context"' internal/finder/server_test.go || sed -i '/"testing"/a \"context"' internal/finder/server_test.go
grep -q '"context"' internal/distribute/sync_test.go || sed -i '/"testing"/a \"context"' internal/distribute/sync_test.go
