// Package r8econf provides file-based configuration loading for the r8e
// resilience library, keeping os and JSON parsing out of the core policy
// package. The core exposes the configuration structs and [r8e.BuildOptions];
// this package reads them from disk.
package r8econf

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/byte4ever/r8e"
)

type (
	// configFile is the top-level JSON structure.
	configFile struct {
		Policies map[string]r8e.PolicyConfig `json:"policies"`
	}

	// Store holds policy configurations loaded from a file along with the
	// [r8e.Registry] that policies built from it register with for readiness
	// reporting. Both fields are unexported so the only way to obtain a usable
	// Store is [Load]; use [Store.Registry] to reach the registry.
	Store struct {
		configs  map[string]r8e.PolicyConfig
		registry *r8e.Registry
	}
)

// Registry returns the [r8e.Registry] that policies built from this store
// register with. Pass it to the readiness handler, e.g.
// r8ehttp.ReadinessHandler(store.Registry()).
func (s *Store) Registry() *r8e.Registry {
	return s.registry
}

// Load reads a JSON configuration file and returns a [Store] of policy
// configurations. Actual [r8e.Policy] instances are not created until
// [GetPolicy] is called, allowing the caller to provide type parameters and
// additional code-level options.
//
// All policies are validated eagerly via [r8e.BuildOptions], so configuration
// errors surface at load time rather than at [GetPolicy].
//
// Duration values (timeout, recovery_timeout, base_delay, max_delay, hedge)
// are parsed using time.ParseDuration. Supported backoff strategies:
// "constant", "exponential", "linear", "exponential_jitter".
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("r8e: read config: %w", err)
	}

	var cfg configFile
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("r8e: parse config: %w", err)
	}

	for name, pc := range cfg.Policies {
		if _, buildErr := r8e.BuildOptions(&pc); buildErr != nil {
			return nil, fmt.Errorf("r8e: policy %q: %w", name, buildErr)
		}
	}

	return &Store{configs: cfg.Policies, registry: r8e.NewRegistry()}, nil
}

// GetPolicy retrieves a named policy configuration from a [Store] and returns a
// typed [r8e.Policy] ready for use. If the name is not found, a bare policy is
// created with only the provided opts. The policy registers with the store's
// [r8e.Registry] for readiness reporting.
//
// Additional options augment or override the config-loaded settings; they are
// applied last so they take precedence. It returns an error if the stored
// configuration for name is invalid (which, since [Load] validates eagerly,
// indicates the Store was modified after loading).
func GetPolicy[T any](
	store *Store,
	name string,
	opts ...r8e.Option,
) (*r8e.Policy[T], error) {
	allOpts := []r8e.Option{r8e.WithRegistry(store.registry)}

	if pc, ok := store.configs[name]; ok {
		configOpts, err := r8e.BuildOptions(&pc)
		if err != nil {
			return nil, fmt.Errorf("r8e: policy %q: %w", name, err)
		}

		allOpts = append(allOpts, configOpts...)
	}

	allOpts = append(allOpts, opts...)

	return r8e.NewPolicy[T](name, allOpts...), nil
}
