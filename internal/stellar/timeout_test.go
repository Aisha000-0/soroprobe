package stellar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHangingServer starts an endpoint that accepts requests and never answers
// them, standing in for an RPC that has stopped responding.
func newHangingServer(t *testing.T) *httptest.Server {
	t.Helper()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})
	return srv
}

func TestHungRequestFailsAfterTheConfiguredTimeout(t *testing.T) {
	t.Parallel()

	srv := newHangingServer(t)
	client, err := New(Options{URL: srv.URL, Timeout: 100 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	start := time.Now()
	_, err = client.GetLatestLedger(context.Background())
	elapsed := time.Since(start)

	require.Error(t, err, "a hung RPC must fail, not hang a CI check forever")
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"the request must survive until the configured timeout fires")
	assert.Less(t, elapsed, 5*time.Second,
		"the error should arrive within roughly the configured timeout")
}

func TestZeroTimeoutFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	// Options.Timeout documents a 30s default for a missing (or non-positive)
	// value; asserting the field keeps the test fast instead of waiting out a
	// real 30s default against a hanging server.
	for _, timeout := range []time.Duration{0, -time.Second} {
		client, err := New(Options{URL: "http://127.0.0.1:0", Timeout: timeout})
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, client.timeout,
			"a zero or negative timeout must fall back to the documented default")
		require.NoError(t, client.Close())
	}
}

// TestCallerDeadlineDoesNotShortenAnInFlightRequest pins today's behaviour,
// which is the one criterion of soroworks/soroprobe#21 a test cannot change:
// the SDK's JSON-RPC channel (jrpc2, jhttp.Channel.Send) posts with
// http.NewRequest — no context — so a context that is already cancelled fails
// fast, but one whose deadline passes after the request is in flight is
// ignored: only http.Client.Timeout (the configured RPC_TIMEOUT) can end it.
// When the transport learns to propagate the caller's context, this test
// fails and must be updated to assert that the shorter deadline wins.
func TestCallerDeadlineDoesNotShortenAnInFlightRequest(t *testing.T) {
	t.Parallel()

	srv := newHangingServer(t)
	const configured = 750 * time.Millisecond
	client, err := New(Options{URL: srv.URL, Timeout: configured})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = client.GetLatestLedger(ctx)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Greater(t, elapsed, configured/2,
		"the in-flight request is bounded by the configured timeout, not by the caller's shorter deadline")
}
