package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// deployedWasmHash reads the fixture's code hash through the API itself, so
// the tests below assert against what the fake serves rather than a literal
// a re-recording would silently invalidate.
func deployedWasmHash(t *testing.T, h http.Handler) string {
	t.Helper()

	var ins probe.InspectResult
	rec := do(t, h, http.MethodGet, "/v1/inspect/"+stellartest.WasmContract, "", &ins)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, ins.WasmHash)
	return ins.WasmHash
}

// checkOutcome returns the outcome of the first check with the given name.
func checkOutcome(r probe.CheckResult, name string) probe.CheckOutcome {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Outcome
		}
	}
	return ""
}

func TestCheckEndpointExpectWasmHash(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.CheckResult
	rec := do(t, h, http.MethodGet,
		"/v1/check/"+stellartest.WasmContract+"?expect_wasm_hash="+deployedWasmHash(t, h),
		"", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, result.OK)
	assert.Equal(t, probe.OutcomePass, checkOutcome(result, "wasm_hash"))
}

func TestCheckEndpointExpectWasmHashMismatchIsStill200(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.CheckResult
	rec := do(t, h, http.MethodGet,
		"/v1/check/"+stellartest.WasmContract+"?expect_wasm_hash="+strings.Repeat("0", 64),
		"", &result)

	// A contract running the wrong code is a successful request reporting
	// bad news, exactly like any other failing check.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, checkOutcome(result, "wasm_hash"))
}

func TestCheckEndpointExpectWasmHashStellarAssetExplains(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.CheckResult
	rec := do(t, h, http.MethodGet,
		"/v1/check/"+stellartest.SACContract+"?expect_wasm_hash="+strings.Repeat("ab", 32),
		"", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, checkOutcome(result, "wasm_hash"))

	var detail string
	for _, c := range result.Checks {
		if c.Name == "wasm_hash" {
			detail = c.Detail
		}
	}
	assert.Contains(t, strings.ToLower(detail), "no wasm")
}

func TestCheckEndpointMalformedExpectWasmHashIs400(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	rec := do(t, h, http.MethodGet,
		"/v1/check/"+stellartest.WasmContract+"?expect_wasm_hash=nope", "", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestChecksEndpointExpectWasmHash: batch entries carry the same assertion in
// the JSON body POST /v1/checks accepts.
func TestChecksEndpointExpectWasmHash(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.BatchResult
	rec := do(t, h, http.MethodPost, "/v1/checks",
		`{"checks":[{"name":"wasm","contract_id":"`+stellartest.WasmContract+
			`","expect_wasm_hash":"`+strings.Repeat("0", 64)+`"}]}`, &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, result.OK)
	require.Len(t, result.Items, 1)
	require.NotNil(t, result.Items[0].Result)
	assert.Equal(t, probe.OutcomeFail, checkOutcome(*result.Items[0].Result, "wasm_hash"))
}
