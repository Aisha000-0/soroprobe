package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func newBookProber(t *testing.T, fake *stellartest.Fake, entries map[string]string) *probe.Prober {
	t.Helper()

	book, err := scval.NewAddressBook(entries)
	require.NoError(t, err)

	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
		AddressBook:   book,
	})
	require.NoError(t, err)
	return p
}

func TestSimulateResolvesNamedAddress(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newBookProber(t, fake, map[string]string{"alice": stellartest.SourceAccount})

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"addr:@alice"},
	})
	require.NoError(t, err)

	// The request is reported as written, and encoded_args names the concrete
	// address so the output is unambiguous.
	assert.Equal(t, []string{"addr:@alice"}, result.Args)
	assert.Equal(t, []string{"addr:" + stellartest.SourceAccount}, result.EncodedArgs)

	args, err := stellartest.ArgsOf(fake.SimulateRequests[0].Transaction)
	require.NoError(t, err)
	require.Len(t, args, 1)
	require.NotNil(t, args[0].Address)
	require.NotNil(t, args[0].Address.AccountId)
}

func TestSimulateResolvesBareName(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newBookProber(t, fake, map[string]string{"alice": stellartest.SourceAccount})

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"@alice"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"addr:" + stellartest.SourceAccount}, result.EncodedArgs)
}

func TestCheckResolvesNamedAddress(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newBookProber(t, fake, map[string]string{"alice": stellartest.SourceAccount})

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"addr:@alice"},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Simulate)

	// check files and the API share this path, so the resolution shows up here
	// exactly as it does for simulate.
	assert.Equal(t, []string{"addr:" + stellartest.SourceAccount}, result.Simulate.EncodedArgs)
}

func TestSimulateUnknownNamedAddress(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newBookProber(t, fake, map[string]string{"alice": stellartest.SourceAccount})

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"addr:@bob"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown address name "@bob"`)
	assert.Contains(t, err.Error(), "known: alice")
	assert.Empty(t, fake.SimulateRequests, "an unknown name must fail before simulation")
}

func TestSimulateLeavesTypedArgumentsUnchanged(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newBookProber(t, fake, map[string]string{"alice": stellartest.SourceAccount})

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"u32:5"},
	})
	require.NoError(t, err)
	assert.Nil(t, result.EncodedArgs, "nothing was rewritten")
}
