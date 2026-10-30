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
	t.Parallel()

	cases := map[string]struct {
		state string
		want  int64
	}{
		"no_breaker":   {"", circuitClosedGauge},
		"closed":       {"closed", circuitClosedGauge},
		"half_open":    {"half_open", circuitHalfOpenGauge},
		"open":         {"open", circuitOpenGauge},
		"ramping":      {"ramping", circuitRampingGauge},
		"future_state": {"future_state", circuitUnknownGauge},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, circuitStateGauge(&r8e.PolicyMetrics{CircuitState: tc.state}))
		})
	}
}

func TestBoolGauge(t *testing.T) {
	healthy := boolGauge(func(m *r8e.PolicyMetrics) bool { return m.Healthy })

	assert.Equal(t, int64(1), healthy(&r8e.PolicyMetrics{Healthy: true}))
	assert.Equal(t, int64(0), healthy(&r8e.PolicyMetrics{Healthy: false}))
}

// erroringMeter is a metric.Meter that fails one call with err: the creation
// of one kind of instrument, or RegisterCallback. It exercises the
// error-propagation paths in Register.
type erroringMeter struct {
	noop.Meter

	err    error
	failOn string // "counter", "gauge", "gauge_f64" or "register"
}

func (m erroringMeter) Int64ObservableCounter(
	name string,
	options ...metric.Int64ObservableCounterOption,
) (metric.Int64ObservableCounter, error) {
	if m.failOn == "counter" {
		return nil, m.err
	}

	return m.Meter.Int64ObservableCounter(name, options...)
}

func (m erroringMeter) Int64ObservableGauge(
	name string,
	options ...metric.Int64ObservableGaugeOption,
) (metric.Int64ObservableGauge, error) {
	if m.failOn == "gauge" {
		return nil, m.err
	}

	return m.Meter.Int64ObservableGauge(name, options...)
}

func (m erroringMeter) Float64ObservableGauge(
	name string,
	options ...metric.Float64ObservableGaugeOption,
) (metric.Float64ObservableGauge, error) {
	if m.failOn == "gauge_f64" {
		return nil, m.err
	}

	return m.Meter.Float64ObservableGauge(name, options...)
}

func (m erroringMeter) RegisterCallback(
	callback metric.Callback,
	instruments ...metric.Observable,
) (metric.Registration, error) {
	if m.failOn == "register" {
		return nil, m.err
	}

	return m.Meter.RegisterCallback(callback, instruments...)
}

func TestRegisterPropagatesInstrumentError(t *testing.T) {
	t.Parallel()

	for _, failOn := range []string{"counter", "gauge", "gauge_f64", "register"} {
		t.Run(failOn, func(t *testing.T) {
			t.Parallel()

			injected := errors.New(failOn + " failed")

			_, err := Register(erroringMeter{err: injected, failOn: failOn}, r8e.NewRegistry())
			require.ErrorIs(t, err, injected)

			if failOn == "register" {
				assert.ErrorContains(t, err, "register otel metrics callback")
			}
		})
	}
}
