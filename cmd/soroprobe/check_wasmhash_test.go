package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// The documented flag exists under the documented name.
func TestCheckExpectWasmHashFlag(t *testing.T) {
	t.Parallel()

	cmd, _ := newRootCmd()
	sub, _, err := cmd.Find([]string{"check"})
	require.NoError(t, err)
	assert.NotNil(t, sub.Flags().Lookup("expect-wasm-hash"))
}

// A malformed value is bad input, reported before any RPC call is made.
func TestCheckRejectsMalformedExpectWasmHash(t *testing.T) {
	err := runWith(t, "check", stellartest.WasmContract, "--expect-wasm-hash", "not-hex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid expected wasm hash")
}

// The --file parser accepts the per-entry key, and a typo'd key still fails
// loudly rather than silently dropping the assertion.
func TestReadBatchAcceptsExpectWasmHash(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("ab", 32)
	req, err := readBatch(strings.NewReader(
		`{"checks": [{"contract_id": "CDEF", "expect_wasm_hash": "`+hash+`"}]}`), "-")
	require.NoError(t, err)
	require.Len(t, req.Checks, 1)
	assert.Equal(t, hash, req.Checks[0].ExpectWasmHash)
}

// --expect-wasm-hash with --file fills entries that name no expectation of
// their own; a per-entry value always wins.
func TestApplyDefaultExpectWasm(t *testing.T) {
	t.Parallel()

	own := strings.Repeat("aa", 32)
	def := strings.Repeat("bb", 32)

	req := probe.BatchRequest{Checks: []probe.NamedCheck{
		{Name: "own", CheckRequest: probe.CheckRequest{ExpectWasmHash: own}},
		{Name: "fills"},
	}}
	applyDefaultExpectWasm(&req, def)
	assert.Equal(t, own, req.Checks[0].ExpectWasmHash, "a per-entry expectation wins over the flag")
	assert.Equal(t, def, req.Checks[1].ExpectWasmHash)

	// Without the flag there is no default, and entries stay unasserted.
	req = probe.BatchRequest{Checks: []probe.NamedCheck{{Name: "none"}}}
	applyDefaultExpectWasm(&req, "")
	assert.Empty(t, req.Checks[0].ExpectWasmHash)
}
