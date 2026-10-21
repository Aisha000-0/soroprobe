package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// percent returns a pointer to v, for the threshold arguments.
func percent(v float64) *float64 { return &v }

// wantFigure is the expected change in one cost figure. A nil percent means
// the figure has no percentage at all, which happens when its baseline was
// zero.
type wantFigure struct {
	delta   int64
	percent *float64
}

func TestCompareCost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseline  probe.Cost
		current   probe.Cost
		threshold *float64
		// want are the expected changes, in the order the figures are
		// reported.
		want         []wantFigure
		wantExceeded bool
	}{
		{
			name:      "an increase is reported in absolute and percentage terms",
			baseline:  probe.Cost{Instructions: 100, ResourceFee: 1000},
			current:   probe.Cost{Instructions: 110, ResourceFee: 1100},
			threshold: percent(10),
			want:      []wantFigure{{10, percent(10)}, {0, nil}, {0, nil}, {100, percent(10)}},
			// Exactly 10% is not more than 10%.
			wantExceeded: false,
		},
		{
			name:         "an increase beyond the threshold is flagged",
			baseline:     probe.Cost{Instructions: 100, ResourceFee: 1000},
			current:      probe.Cost{Instructions: 111, ResourceFee: 1100},
			threshold:    percent(10),
			want:         []wantFigure{{11, percent(11)}, {0, nil}, {0, nil}, {100, percent(10)}},
			wantExceeded: true,
		},
		{
			name:         "a decrease is never an increase",
			baseline:     probe.Cost{Instructions: 100, ResourceFee: 1000},
			current:      probe.Cost{Instructions: 90, ResourceFee: 900},
			threshold:    percent(10),
			want:         []wantFigure{{-10, percent(-10)}, {0, nil}, {0, nil}, {-100, percent(-10)}},
			wantExceeded: false,
		},
		{
			name:         "an unchanged cost is not an increase",
			baseline:     probe.Cost{Instructions: 100, ResourceFee: 1000},
			current:      probe.Cost{Instructions: 100, ResourceFee: 1000},
			threshold:    percent(0),
			want:         []wantFigure{{0, percent(0)}, {0, nil}, {0, nil}, {0, percent(0)}},
			wantExceeded: false,
		},
		{
			name:     "without a threshold nothing is flagged",
			baseline: probe.Cost{Instructions: 100, ResourceFee: 1000},
			current:  probe.Cost{Instructions: 1000, ResourceFee: 10000},
			want:     []wantFigure{{900, percent(900)}, {0, nil}, {0, nil}, {9000, percent(900)}},
			// The percentages are still reported; only the verdict is
			// missing.
			wantExceeded: false,
		},
		{
			name:      "a zero baseline has no percentage to compare against",
			baseline:  probe.Cost{},
			current:   probe.Cost{Instructions: 5},
			threshold: percent(1000),
			want:      []wantFigure{{5, nil}, {0, nil}, {0, nil}, {0, nil}},
			// From nothing to something is not a small change, so it counts
			// as an increase whatever the threshold is.
			wantExceeded: true,
		},
		{
			name:         "a zero baseline that stays zero is not an increase",
			baseline:     probe.Cost{},
			current:      probe.Cost{},
			threshold:    percent(10),
			want:         []wantFigure{{0, nil}, {0, nil}, {0, nil}, {0, nil}},
			wantExceeded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := probe.CompareCost(tt.baseline, tt.current, tt.threshold)

			require.Len(t, got.Figures, len(tt.want))
			assert.Equal(t, tt.wantExceeded, got.Exceeded)

			for i, f := range got.Figures {
				assert.Equal(t, tt.want[i].delta, f.Delta, "figure %q", f.Name)

				if tt.want[i].percent == nil {
					assert.Nil(t, f.Percent, "figure %q has no percentage", f.Name)
					continue
				}
				require.NotNil(t, f.Percent, "figure %q", f.Name)
				assert.InDelta(t, *tt.want[i].percent, *f.Percent, 0.0001, "figure %q", f.Name)
			}
		})
	}
}

func TestCompareCostReportsTheFiguresInAStableOrder(t *testing.T) {
	t.Parallel()

	got := probe.CompareCost(probe.Cost{}, probe.Cost{}, nil)

	names := make([]string, 0, len(got.Figures))
	for _, f := range got.Figures {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{
		"cpu instructions",
		"disk read bytes",
		"write bytes",
		"resource fee (stroops)",
	}, names)
}

func TestSimulateComparesAgainstABaseline(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Baseline: &probe.Baseline{
			ContractID: stellartest.SACContract,
			Function:   "decimals",
			Cost:       probe.Cost{Instructions: 1},
		},
	})
	require.NoError(t, err)

	require.NotNil(t, result.Comparison)
	require.Len(t, result.Comparison.Figures, 4)
	// The recorded simulation used more than one instruction, so the figure
	// grew.
	assert.Positive(t, result.Comparison.Figures[0].Delta)
	assert.False(t, result.Comparison.Exceeded, "no threshold means no verdict")
}

func TestSimulateRejectsABaselineForAnotherContract(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Baseline: &probe.Baseline{
			ContractID: stellartest.WasmContract,
			Function:   "decimals",
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrBaselineMismatch)
	assert.Contains(t, err.Error(), stellartest.WasmContract)

	// The mismatch must be caught before anything is sent: a comparison
	// against the wrong call would look like a measurement.
	assert.Empty(t, fake.SimulateRequests)
}

func TestSimulateRejectsABaselineForAnotherFunction(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Baseline: &probe.Baseline{
			ContractID: stellartest.SACContract,
			Function:   "balance",
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrBaselineMismatch)
	assert.Contains(t, err.Error(), "balance")
	assert.Empty(t, fake.SimulateRequests)
}

func TestSimulateRejectsABaselineThatNamesNoCall(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Baseline:   &probe.Baseline{},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrBaselineMismatch)
	assert.Empty(t, fake.SimulateRequests)
}

func TestSimulateWithoutABaselineHasNoComparison(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
	})
	require.NoError(t, err)
	assert.Nil(t, result.Comparison)
}
