package r8eotel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/byte4ever/r8e"
)

func TestCircuitStateGauge(t *testing.T) {
	cases := map[string]int64{
		"":          circuitClosedGauge,
		"closed":    circuitClosedGauge,
		"half_open": circuitHalfOpenGauge,
		"open":      circuitOpenGauge,
	}

	for state, want := range cases {
		assert.Equal(t, want, circuitStateGauge(&r8e.PolicyMetrics{CircuitState: state}))
	}
}

func TestBoolGauge(t *testing.T) {
	healthy := boolGauge(func(m *r8e.PolicyMetrics) bool { return m.Healthy })

	assert.Equal(t, int64(1), healthy(&r8e.PolicyMetrics{Healthy: true}))
	assert.Equal(t, int64(0), healthy(&r8e.PolicyMetrics{Healthy: false}))
}

// erroringMeter is a metric.Meter that fails to create one kind of instrument,
// so the error-propagation paths in Register can be exercised.
type erroringMeter struct {
	noop.Meter

	failOn string // "counter" or "gauge"
}

func (m erroringMeter) Int64ObservableCounter(
	name string,
	options ...metric.Int64ObservableCounterOption,
) (metric.Int64ObservableCounter, error) {
	if m.failOn == "counter" {
		return nil, errors.New("counter create failed")
	}

	return m.Meter.Int64ObservableCounter(name, options...)
}

func (m erroringMeter) Int64ObservableGauge(
	name string,
	options ...metric.Int64ObservableGaugeOption,
) (metric.Int64ObservableGauge, error) {
	if m.failOn == "gauge" {
		return nil, errors.New("gauge create failed")
	}

	return m.Meter.Int64ObservableGauge(name, options...)
}

func (m erroringMeter) RegisterCallback(
	callback metric.Callback,
	instruments ...metric.Observable,
) (metric.Registration, error) {
	if m.failOn == "register" {
		return nil, errors.New("register callback failed")
	}

	return m.Meter.RegisterCallback(callback, instruments...)
}

func TestRegisterPropagatesInstrumentError(t *testing.T) {
	reg := r8e.NewRegistry()

	cases := map[string]string{
		"counter":  "counter create failed",
		"gauge":    "gauge create failed",
		"register": "register otel metrics callback", // wrapped
	}

	for failOn, wantSub := range cases {
		_, err := Register(erroringMeter{failOn: failOn}, reg)
		require.Error(t, err, "failOn=%s", failOn)
		require.ErrorContains(t, err, wantSub)
	}
}
