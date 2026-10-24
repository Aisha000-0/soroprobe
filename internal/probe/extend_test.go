package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// extendFee is the resource fee the recorded extension simulation reports.
const extendFee = 6128

// addDataEntry puts a data entry on the fake's ledger, so a test can see
// what happens to an entry that is actually there.
func addDataEntry(t *testing.T, fake *stellartest.Fake, contractID, spec string, liveUntil uint32) {
	t.Helper()

	contract, err := stellar.ParseContractID(contractID)
	require.NoError(t, err)

	codec := scval.NewRegistry()
	val, err := codec.Encode(spec)
	require.NoError(t, err)

	key, err := stellar.EncodeKey(stellar.DataKey(contract, val, xdr.ContractDataDurabilityPersistent))
	require.NoError(t, err)

	fake.Entries[key] = protocol.LedgerEntryResult{
		KeyXDR:             key,
		LiveUntilLedgerSeq: &liveUntil,
	}
}

func TestInspectEstimatesTheCostOfExtending(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.SACContract,
		EstimateExtend: 1_000_000,
	})
	require.NoError(t, err)

	// A Stellar Asset Contract has an instance entry and no code entry.
	require.Len(t, result.ExtendEstimates, 1)

	estimate := result.ExtendEstimates[0]
	assert.Equal(t, "instance", estimate.Kind)
	assert.EqualValues(t, 1_000_000, estimate.Ledgers)
	assert.EqualValues(t, fake.LatestLedger+1_000_000, estimate.ExtendTo)
	assert.EqualValues(t, extendFee, estimate.ResourceFee)
	assert.EqualValues(t, extendFee, estimate.MinResourceFee)
	assert.Empty(t, estimate.Note)

	// The extension is simulated and never submitted.
	require.Len(t, fake.SimulateRequests, 1)
	assert.Equal(t, []string{stellartest.ExtendFootprintTTL}, fake.Functions)
}

func TestInspectEstimatesTheCodeEntryOfAWasmContract(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.WasmContract,
		EstimateExtend: 500_000,
	})
	require.NoError(t, err)

	// A Wasm contract has an instance entry and a code entry, each with its
	// own TTL, so each is priced on its own.
	require.Len(t, result.ExtendEstimates, 2)
	assert.Equal(t, "instance", result.ExtendEstimates[0].Kind)
	assert.Equal(t, "code", result.ExtendEstimates[1].Kind)
	for _, e := range result.ExtendEstimates {
		assert.Empty(t, e.Note)
		assert.EqualValues(t, extendFee, e.ResourceFee)
	}
}

func TestInspectSaysSoWhenAnEntryCannotBeExtended(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	// No fixture holds a data entry, so it is missing from the ledger.
	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.SACContract,
		DataKeys:       []string{"sym:Balance"},
		EstimateExtend: 1_000_000,
	})
	require.NoError(t, err)

	require.Len(t, result.ExtendEstimates, 2)

	instance := result.ExtendEstimates[0]
	assert.Equal(t, "instance", instance.Kind)
	assert.Empty(t, instance.Note)
	assert.EqualValues(t, extendFee, instance.ResourceFee)

	// A missing entry has nothing to extend, and a price for it would be a
	// number about nothing.
	data := result.ExtendEstimates[1]
	assert.Equal(t, "data", data.Kind)
	assert.Equal(t, "sym:Balance", data.Key)
	assert.NotEmpty(t, data.Note)
	assert.Zero(t, data.ResourceFee)
}

func TestInspectSaysSoWhenAnEntryHasExpired(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)
	fake.SetTTL(t, instanceKey, fake.LatestLedger-1)

	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.SACContract,
		EstimateExtend: 1_000_000,
	})
	require.NoError(t, err)

	require.Len(t, result.ExtendEstimates, 1)
	estimate := result.ExtendEstimates[0]
	assert.NotEmpty(t, estimate.Note)
	assert.Contains(t, estimate.Note, "expired")
	assert.Zero(t, estimate.ResourceFee)

	// An expired entry cannot be extended, so nothing was simulated.
	assert.Empty(t, fake.SimulateRequests)
}

func TestInspectEstimatesAnExistingDataEntry(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	addDataEntry(t, fake, stellartest.SACContract, "sym:Balance", fake.LatestLedger+1_000_000)

	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.SACContract,
		DataKeys:       []string{"sym:Balance"},
		EstimateExtend: 1_000_000,
	})
	require.NoError(t, err)

	require.Len(t, result.ExtendEstimates, 2)
	data := result.ExtendEstimates[1]
	assert.Equal(t, "data", data.Kind)
	assert.Equal(t, "sym:Balance", data.Key)
	assert.Empty(t, data.Note)
	assert.EqualValues(t, extendFee, data.ResourceFee)
}

func TestInspectWithoutAnEstimateMakesNoExtensionSimulation(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.SACContract,
	})
	require.NoError(t, err)

	assert.Empty(t, result.ExtendEstimates)
	assert.Empty(t, fake.SimulateRequests)
}

func TestInspectDoesNotEstimateForAnUndeployedContract(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.UndeployedContract,
		EstimateExtend: 1_000_000,
	})
	require.NoError(t, err)

	assert.False(t, result.Deployed)
	assert.Empty(t, result.ExtendEstimates)
	assert.Empty(t, fake.SimulateRequests)
}
