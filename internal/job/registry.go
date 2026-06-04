package job

import (
	"fmt"
	"sync"
)

// Registry holds all registered job definitions
type Registry struct {
	mu      sync.RWMutex
	jobs    map[string]*Definition
	ordered []string
}

// NewRegistry creates a new empty job registry
func NewRegistry() *Registry {
	return &Registry{
		jobs:    make(map[string]*Definition),
		ordered: make([]string, 0),
	}
}

// Register adds a job definition to the registry
func (r *Registry) Register(def *Definition) error {
	if def.Kind == "" {
		return fmt.Errorf("job kind cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.jobs[def.Kind]; exists {
		return fmt.Errorf("job %q already registered", def.Kind)
	}

	r.jobs[def.Kind] = def
	r.ordered = append(r.ordered, def.Kind)

	return nil
}

// Get retrieves a job definition by kind
func (r *Registry) Get(kind string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, ok := r.jobs[kind]
	if !ok {
		return nil, fmt.Errorf("job %q not found", kind)
	}
	return def, nil
}

// GetAll returns all registered job definitions in registration order
func (r *Registry) GetAll() []*Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	jobs := make([]*Definition, 0, len(r.ordered))
	for _, kind := range r.ordered {
		jobs = append(jobs, r.jobs[kind])
	}
	return jobs
}
