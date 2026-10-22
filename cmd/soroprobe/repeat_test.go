package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// varyingClient wraps the fixture fake and rewrites the CPU instruction count
// of each successive simulation from costs. It is how a test produces the run
// to run variation that --repeat exists to report: the recorded fixtures are
// identical every time, so without this there is never any spread.
type varyingClient struct {
	*stellartest.Fake
	costs []uint32
	n     int
}

// calls reports how many simulations were performed.
func (c *varyingClient) calls() int { return c.n }

func (c *varyingClient) SimulateTransaction(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	resp, err := c.Fake.SimulateTransaction(ctx, req)
	if err != nil || resp.TransactionDataXDR == "" || len(c.costs) == 0 {
		return resp, err
	}

	var data xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(resp.TransactionDataXDR, &data); err != nil {
		return resp, err
	}
	idx := c.n
	if idx >= len(c.costs) {
		idx = len(c.costs) - 1
	}
	costs := c.costs
	c.n++

	data.Resources.Instructions = xdr.Uint32(costs[idx])

	encoded, err := xdr.MarshalBase64(data)
	if err != nil {
		return resp, err
	}
	resp.TransactionDataXDR = encoded
	return resp, nil
}

// runRepeat drives `simulate --repeat` end to end with a client whose cost
// varies per call, and returns stdout and the command error.
func runRepeat(t *testing.T, costs []uint32, args ...string) (string, error) {
	t.Helper()
	out, _, err := runRepeatClient(t, costs, args...)
	return out, err
}

// runRepeatClient is runRepeat, and also hands back the client so a test can
// assert how many simulations were actually attempted.
func runRepeatClient(t *testing.T, costs []uint32, args ...string) (string, *varyingClient, error) {
	t.Helper()
	t.Chdir(t.TempDir())

	fake := stellartest.NewFake(t)
	client := &varyingClient{Fake: fake, costs: costs}

	cmd, opts := newRootCmd()
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
		p, err := probe.New(probe.Options{
			Client:        client,
			SourceAccount: cfg.SourceAccount,
			Thresholds:    health.Thresholds{Warn: cfg.WarnLedgers, Critical: cfg.CriticalLedgers},
		})
		return p, func() {}, err
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), client, err
}

func TestSimulateRepeatReportsTheSpreadAndRunsEveryTime(t *testing.T) {
	fakeCosts := []uint32{100, 130, 120}

	out, client, err := runRepeatClient(t, fakeCosts, "simulate", stellartest.SACContract, "decimals", "--repeat", "3")
	require.NoError(t, err)
	assert.Equal(t, 3, client.calls(), "every run must actually be simulated")

	assert.Contains(t, out, "cost across 3 runs")
	assert.Contains(t, out, "100..130")
	assert.Contains(t, out, "spread 30")
}

func TestSimulateRepeatJSONCarriesTheSpread(t *testing.T) {
	out, err := runRepeat(t, []uint32{100, 130, 120},
		"--json", "simulate", stellartest.SACContract, "decimals", "--repeat", "3")
	require.NoError(t, err)

	var result probe.SimulateResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.NotNil(t, result.Spread)
	assert.Equal(t, 3, result.Spread.Runs)

	var cpu *probe.FigureSpread
	for i := range result.Spread.Figures {
		if result.Spread.Figures[i].Name == "cpu instructions" {
			cpu = &result.Spread.Figures[i]
		}
	}
	require.NotNil(t, cpu)
	assert.Equal(t, int64(100), cpu.Min)
	assert.Equal(t, int64(130), cpu.Max)
}

func TestSimulateRepeatIsSilentForOneRun(t *testing.T) {
	out, err := runRepeat(t, []uint32{100}, "simulate", stellartest.SACContract, "decimals")
	require.NoError(t, err)

	assert.NotContains(t, out, "cost across", "a single run has no spread to report")
}

func TestSimulateRepeatExitsOneWhenNoisierThanTheThreshold(t *testing.T) {
	out, err := runRepeat(t, []uint32{100, 200},
		"simulate", stellartest.SACContract, "decimals", "--repeat", "2", "--max-spread", "5%")
	require.Error(t, err)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "varied by more than 5%")
}

func TestSimulateRepeatWithinTheThresholdPasses(t *testing.T) {
	_, err := runRepeat(t, []uint32{100, 102},
		"simulate", stellartest.SACContract, "decimals", "--repeat", "2", "--max-spread", "5%")
	require.NoError(t, err)
}

func TestSimulateRepeatThresholdNeedsRepeatedRuns(t *testing.T) {
	_, err := runRepeat(t, nil,
		"simulate", stellartest.SACContract, "decimals", "--max-spread", "5%")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-spread needs --repeat")

	_, err = runRepeat(t, nil,
		"simulate", stellartest.SACContract, "decimals", "--repeat", "1", "--max-spread", "5%")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--max-spread needs --repeat")
}

func TestSimulateRepeatRejectsANonPercentageSpread(t *testing.T) {
	_, err := runRepeat(t, nil,
		"simulate", stellartest.SACContract, "decimals", "--repeat", "2", "--max-spread", "lots")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a percentage")
}

func TestSimulateRepeatIsRejectedWithFile(t *testing.T) {
	fake := stellartest.NewFake(t)

	for name, args := range map[string][]string{
		"repeat":     {"simulate", "--file", "calls.json", "--repeat", "3"},
		"max-spread": {"simulate", "--file", "calls.json", "--max-spread", "5%"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runWithFake(t, fake, args...)
			require.Error(t, err)
		})
	}
}

func TestSimulateRepeatUsesTheMaximumAsTheBaseline(t *testing.T) {
	// A baseline of 100 with runs at 100, 150, 130: the comparison reference
	// is the maximum (150), so a 10% limit is exceeded even though the last
	// run (130) alone would be under it.
	baseline := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost":        map[string]any{"instructions": 100, "resource_fee_stroops": 1000},
	})

	out, err := runRepeat(t, []uint32{100, 150, 130},
		"simulate", stellartest.SACContract, "decimals",
		"--repeat", "3", "--baseline", baseline, "--max-increase", "10%")
	require.Error(t, err)
	assertExitCodeOne(t, err)

	// The text report prints the comparison; asserting on the rendered text
	// is clearer than re-parsing stdout.
	assert.Contains(t, out, "cost compared to the baseline")
	assert.Contains(t, out, "more than 10%")
}

func TestSimulateRepeatPropagatesATransportFailure(t *testing.T) {
	t.Chdir(t.TempDir())

	fake := stellartest.NewFake(t)
	fake.SimulateErr = errors.New("rpc unreachable")

	cmd, opts := newRootCmd()
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
		p, err := probe.New(probe.Options{Client: fake, SourceAccount: cfg.SourceAccount})
		return p, func() {}, err
	}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"simulate", stellartest.SACContract, "decimals", "--repeat", "3"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "rpc unreachable")
}
