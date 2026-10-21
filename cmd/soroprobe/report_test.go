package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReportEmptyPathMeansNoReport(t *testing.T) {
	t.Parallel()

	r, err := openReport("")
	require.NoError(t, err)
	require.Nil(t, r)

	// The nil report must stay a no-op, so call sites need no branching.
	assert.NoError(t, r.write(map[string]string{"ignored": "yes"}))
	assert.NoError(t, r.close())
}

func TestReportFileWritesJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "report.json")
	r, err := openReport(path)
	require.NoError(t, err)
	require.NoError(t, r.write(map[string]any{"ok": true}))
	require.NoError(t, r.close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, true, got["ok"])
	assert.Contains(t, string(data), "\n  ", "the report is indented like --json output")
}

func TestOpenReportFailsForAnUnwritablePath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing", "report.json")

	_, err := openReport(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "report file")
}

func TestUnwritableReportFailsBeforeAnyNetworkCall(t *testing.T) {
	// The directory does not exist, so creating the report must fail before a
	// prober is built or any RPC call is made.
	path := filepath.Join(t.TempDir(), "missing", "report.json")

	commands := [][]string{
		{"inspect", "CDLZ", "--report", path},
		{"check", "CDLZ", "--report", path},
		{"simulate", "CDLZ", "decimals", "--report", path},
		{"check", "--file", "-", "--report", path},
	}
	for _, args := range commands {
		t.Run(args[0]+"/"+args[1], func(t *testing.T) {
			err := runWith(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "report file")
		})
	}
}
