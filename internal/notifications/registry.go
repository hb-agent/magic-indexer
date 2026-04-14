package notifications

import "sync"

// Registry maps collection NSID to the Notifier responsible for it.
type Registry struct {
	mu    sync.RWMutex
	byNSID map[string]Notifier
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byNSID: make(map[string]Notifier)}
}

// Register adds a notifier. If two notifiers register for the same collection,
// the second overwrites the first.
func (r *Registry) Register(n Notifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byNSID[n.Collection()] = n
}

// Get returns the notifier for a collection, if registered.
func (r *Registry) Get(collection string) (Notifier, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.byNSID[collection]
	return n, ok
}
