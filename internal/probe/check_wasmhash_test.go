package probe_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// fixtureWasmHash reads the code hash the recorded wasm fixtures serve, via a
// throwaway prober, so a test's own fake starts with no recorded requests.
func fixtureWasmHash(t *testing.T) string {
	t.Helper()

	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)
	ins, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)
	require.NotEmpty(t, ins.WasmHash)
	return ins.WasmHash
}

// checkNames lists check names in the order they were evaluated.
func checkNames(r *probe.CheckResult) []string {
	names := make([]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		names = append(names, c.Name)
	}
	return names
}

// checkDetail returns the detail of the first check with the given name.
func checkDetail(r *probe.CheckResult, name string) string {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Detail
		}
	}
	return ""
}

// TestCheckExpectWasmHashPasses covers the post-deploy happy path: the step
// sits right after deployed, and compares against the hash inspect already
// read, so it costs no RPC calls of its own.
func TestCheckExpectWasmHashPasses(t *testing.T) {
	t.Parallel()

	want := fixtureWasmHash(t)

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	// Uppercase and space-padded on purpose: a pipeline may print the hash
	// in any case, and a value pasted from a file can carry stray whitespace.
	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID:     stellartest.WasmContract,
		ExpectWasmHash: " " + strings.ToUpper(want) + " ",
	})
	require.NoError(t, err)

	assert.True(t, result.OK)
	assert.Equal(t, probe.OutcomePass, outcomes(result)["wasm_hash"])
	assert.Contains(t, checkDetail(result, "wasm_hash"), "the expected wasm hash")

	// The hash comes from the instance entry inspect already read, so the
	// step adds no ledger lookups: one round for the instance, one for the
	// code entry.
	assert.Len(t, fake.EntryRequests, 2)

	// wasm_hash is evaluated after deployed and before the TTL steps.
	names := checkNames(result)
	wasmIdx := slices.Index(names, "wasm_hash")
	require.Greater(t, wasmIdx, slices.Index(names, "deployed"))
	require.Greater(t, slices.Index(names, "instance_ttl"), wasmIdx)
}

// TestCheckExpectWasmHashMismatchFails is the upgrade-pipeline case: the
// contract runs code other than what was expected, and that fails the check.
func TestCheckExpectWasmHashMismatchFails(t *testing.T) {
	t.Parallel()

	actual := fixtureWasmHash(t)
	stale := strings.Repeat("0", 64)

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID:     stellartest.WasmContract,
		ExpectWasmHash: stale,
	})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, outcomes(result)["wasm_hash"])

	detail := checkDetail(result, "wasm_hash")
	assert.Contains(t, detail, actual, "the detail must name the code that is running")
	assert.Contains(t, detail, stale, "and the code that was expected")

	// A failed step stops the run, so nothing downstream is asserted on
	// code already known to be wrong.
	assert.NotContains(t, checkNames(result), "instance_ttl")
}

// TestCheckExpectWasmHashStellarAssetFails: a Stellar Asset Contract has no
// wasm, so the assertion cannot hold and the step must say why.
func TestCheckExpectWasmHashStellarAssetFails(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID:     stellartest.SACContract,
		ExpectWasmHash: fixtureWasmHash(t),
	})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, outcomes(result)["wasm_hash"])

	detail := strings.ToLower(checkDetail(result, "wasm_hash"))
	assert.Contains(t, detail, "stellar asset")
	assert.Contains(t, detail, "no wasm")
}

// TestCheckExpectWasmHashInvalidIsBadInput: a malformed hash is a problem
// with the request, not with the contract. It is rejected before any RPC
// call and stays distinguishable from a failing check.
func TestCheckExpectWasmHashInvalidIsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hash string
	}{
		{"not hex", "hello"},
		{"too short", strings.Repeat("a", 63)},
		{"too long", strings.Repeat("a", 65)},
		{"right length, not hex", strings.Repeat("z", 64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := stellartest.NewFake(t)
			p := newProber(t, fake, health.DefaultThresholds)

			_, err := p.Check(context.Background(), probe.CheckRequest{
				ContractID:     stellartest.WasmContract,
				ExpectWasmHash: tt.hash,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, probe.ErrInvalidWasmHash)
			assert.Empty(t, fake.EntryRequests, "bad input must be rejected before any RPC call")
		})
	}
}

// TestCheckWithoutExpectationAddsNoStep: checks that do not ask for the
// assertion keep exactly the output they had before it existed.
func TestCheckWithoutExpectationAddsNoStep(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)

	assert.NotContains(t, checkNames(result), "wasm_hash")
}

// TestCheckBatchExpectWasmHash: the assertion travels per entry through a
// batch, so one deploy can pin every contract to the build it shipped.
func TestCheckBatchExpectWasmHash(t *testing.T) {
	t.Parallel()

	want := fixtureWasmHash(t)

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		{
			Name:         "current",
			CheckRequest: probe.CheckRequest{ContractID: stellartest.WasmContract, ExpectWasmHash: want},
		},
		{
			Name:         "stale",
			CheckRequest: probe.CheckRequest{ContractID: stellartest.WasmContract, ExpectWasmHash: strings.Repeat("0", 64)},
		},
	}})
	require.NoError(t, err)

	require.Len(t, result.Items, 2)
	require.NotNil(t, result.Items[0].Result)
	assert.True(t, result.Items[0].Result.OK)
	assert.Equal(t, probe.OutcomePass, outcomes(result.Items[0].Result)["wasm_hash"])
	require.NotNil(t, result.Items[1].Result)
	assert.False(t, result.Items[1].Result.OK)
	assert.Equal(t, probe.OutcomeFail, outcomes(result.Items[1].Result)["wasm_hash"])
	assert.False(t, result.OK)
}

// TestCheckBatchRejectsInvalidExpectWasmHash: a malformed entry is a
// malformed batch — nothing is checked.
func TestCheckBatchRejectsInvalidExpectWasmHash(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		{
			Name: "typo",
			CheckRequest: probe.CheckRequest{
				ContractID:     stellartest.WasmContract,
				ExpectWasmHash: "not-hex",
			},
		},
	}})
	require.ErrorIs(t, err, probe.ErrInvalidBatch)
	assert.Contains(t, err.Error(), "invalid expected wasm hash")
	assert.Empty(t, fake.EntryRequests, "a malformed batch sends nothing")
}
