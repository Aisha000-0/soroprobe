package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// inspectStub serves the recorded ledger entries and the recorded extension
// simulation, so the CLI can be driven end to end without a network.
func inspectStub(t *testing.T) *httptest.Server {
	t.Helper()

	entries := stellartest.LoadLedgerEntries(t, "ledger_entries_sac_instance.json")
	extend := stellartest.LoadSimulate(t, "simulate_extend.json")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		var result any
		switch req.Method {
		case "getLedgerEntries":
			result = entries
		case "simulateTransaction":
			result = extend
		default:
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			}))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": result,
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestInspectPricesTheExtensionOfEachEntry(t *testing.T) {
	server := inspectStub(t)

	t.Chdir(t.TempDir())

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"inspect",
		"--rpc-url", server.URL,
		"--source-account", stellartest.SourceAccount,
		"--estimate-extend", "1000000",
		stellartest.SACContract,
	})

	require.NoError(t, cmd.Execute())

	text := out.String()
	assert.Contains(t, text, "cost to extend")
	assert.Contains(t, text, "instance")
	// The recorded extension simulation costs 6128 stroops.
	assert.Contains(t, text, "6128 stroops")
}

func TestInspectWithoutTheFlagPricesNothing(t *testing.T) {
	server := inspectStub(t)

	t.Chdir(t.TempDir())

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"inspect",
		"--rpc-url", server.URL,
		"--source-account", stellartest.SourceAccount,
		stellartest.SACContract,
	})

	require.NoError(t, cmd.Execute())
	assert.NotContains(t, out.String(), "cost to extend")
}
