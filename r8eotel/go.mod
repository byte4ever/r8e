module github.com/byte4ever/r8e/r8eotel

go 1.25.5

require (
	// Mapping uses the read-through cache metrics on r8e.PolicyMetrics added after
	// v0.5.0; until the next core release the local replace below supplies them,
	// and this require floor bumps at that release (as for prior versions).
	github.com/byte4ever/r8e v0.5.0
	github.com/stretchr/testify v1.11.1
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/metric v1.44.0
	go.opentelemetry.io/otel/sdk/metric v1.44.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Local monorepo development convenience; ignored by external consumers, which
// resolve the require version above.
replace github.com/byte4ever/r8e => ../
