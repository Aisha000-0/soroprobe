package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// testnetPassphrase is the network the fixture fake serves, and the default
// the rest of the suite configures its Prober for.
const testnetPassphrase = "Test SDF Network ; September 2015"

func TestReadyHealthyEndpoint(t *testing.T) {
	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.Thresholds{})

	require.NoError(t, p.Ready(context.Background(), testnetPassphrase))
	assert.Equal(t, 1, fake.HealthRequests)
	assert.Equal(t, 1, fake.NetworkRequests)
}

func TestReadyUnhealthyRPC(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.HealthStatus = "unhealthy"
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnhealthy)
	assert.Contains(t, err.Error(), "unhealthy")

	// A failed health check must not spend a second call asking which
	// network the endpoint serves.
	assert.Equal(t, 0, fake.NetworkRequests)
}

func TestReadyUnreachableRPC(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.HealthErr = errors.New("connection refused")
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestReadyUnreachableOnGetNetwork(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.NetworkErr = errors.New("connection refused")
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
}

func TestReadyWrongNetwork(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrWrongNetwork)
	// The reason must quote both networks: an operator has to be able to
	// see what the endpoint serves and what was expected.
	assert.Contains(t, err.Error(), "Public Global Stellar Network")
	assert.Contains(t, err.Error(), testnetPassphrase)
}
