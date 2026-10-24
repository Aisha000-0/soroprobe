package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// simulateStub serves the recorded simulateTransaction response, so the CLI
// can be driven end to end without a network.
func simulateStub(t *testing.T) *httptest.Server {
	t.Helper()

	fixture := stellartest.LoadSimulate(t, "simulate_success.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": fixture,
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

// runSimulate runs the simulate command against the stub and returns its
// output and error.
func runSimulate(t *testing.T, server *httptest.Server, args ...string) (string, error) {
	t.Helper()

	t.Chdir(t.TempDir())

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{
		"simulate",
		"--rpc-url", server.URL,
		"--source-account", stellartest.SourceAccount,
		stellartest.SACContract,
		"decimals",
	}, args...))

	err := cmd.Execute()
	return out.String(), err
}

// writeBaseline writes a saved report to a file and returns its path.
func writeBaseline(t *testing.T, doc map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(doc)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "baseline.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// recordedCost is the cost of the recorded simulation: 127569 instructions
// and a 12279 stroop resource fee.
func recordedCost() map[string]any {
	return map[string]any{
		"instructions":             127569,
		"disk_read_bytes":          0,
		"write_bytes":              0,
		"resource_fee_stroops":     12279,
		"min_resource_fee_stroops": 12279,
	}
}

func TestSimulateComparesAgainstABaseline(t *testing.T) {
	server := simulateStub(t)

	path := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost":        recordedCost(),
	})

	out, err := runSimulate(t, server, "--baseline", path)
	require.NoError(t, err)

	assert.Contains(t, out, "cost compared to the baseline")
	assert.Contains(t, out, "cpu instructions")
	assert.Contains(t, out, "127569 -> 127569")
}

func TestSimulateFailsWhenTheCostGrowsBeyondTheThreshold(t *testing.T) {
	server := simulateStub(t)

	// A baseline from a cheaper version of the call.
	path := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost": map[string]any{
			"instructions":         1000,
			"resource_fee_stroops": 1000,
		},
	})

	out, err := runSimulate(t, server, "--baseline", path, "--max-increase", "10%")
	require.Error(t, err)

	// The exit code must be 1, not 2: the tool ran fine and the contract
	// got more expensive.
	var failed *checkFailedError
	require.True(t, errors.As(err, &failed), "expected a check failure, got %v", err)
	assert.Contains(t, out, "more than 10%")
}

func TestSimulateAcceptsACostThatGrowsWithinTheThreshold(t *testing.T) {
	server := simulateStub(t)

	path := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost":        recordedCost(),
	})

	_, err := runSimulate(t, server, "--baseline", path, "--max-increase", "10%")
	require.NoError(t, err)
}

func TestSimulateRejectsABaselineForAnotherCall(t *testing.T) {
	server := simulateStub(t)

	path := writeBaseline(t, map[string]any{
		"contract_id": stellartest.WasmContract,
		"function":    "decimals",
		"cost":        recordedCost(),
	})

	_, err := runSimulate(t, server, "--baseline", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseline")
}

func TestMaxIncreaseNeedsABaseline(t *testing.T) {
	server := simulateStub(t)

	_, err := runSimulate(t, server, "--max-increase", "10%")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--baseline")
}

func TestParseMaxIncrease(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{name: "a bare number is a percentage", input: "10", want: 10},
		{name: "the percent sign is optional", input: "10%", want: 10},
		{name: "a fraction is allowed", input: "0.5%", want: 0.5},
		{name: "whitespace is trimmed", input: " 25 % ", want: 25},
		{name: "zero is allowed", input: "0%", want: 0},
		{name: "not a number", input: "many", wantErr: true},
		{name: "negative", input: "-5%", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMaxIncrease(tt.input, true)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.InDelta(t, tt.want, *got, 0.0001)
		})
	}
}

func TestParseMaxIncreaseWithoutABaselineIsRejected(t *testing.T) {
	_, err := parseMaxIncrease("10%", false)
	require.Error(t, err)
}

func TestReadBaselineReadsASavedReport(t *testing.T) {
	path := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost":        recordedCost(),
		// A saved report holds more than the baseline needs; the rest must
		// not stop it from being read.
		"success": true,
	})

	baseline, err := readBaseline(path)
	require.NoError(t, err)
	require.NotNil(t, baseline)
	assert.Equal(t, stellartest.SACContract, baseline.ContractID)
	assert.Equal(t, "decimals", baseline.Function)
	assert.EqualValues(t, 127569, baseline.Cost.Instructions)
}

func TestReadBaselineWithNoPathIsNoBaseline(t *testing.T) {
	baseline, err := readBaseline("")
	require.NoError(t, err)
	assert.Nil(t, baseline)
}

func TestReadBaselineRejectsAMissingFile(t *testing.T) {
	_, err := readBaseline(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
}

func TestReadBaselineRejectsAMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, err := readBaseline(path)
	require.Error(t, err)
}
