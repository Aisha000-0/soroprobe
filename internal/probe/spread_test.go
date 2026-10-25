package probe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fp(readOnly, readWrite []string) Footprint {
	return Footprint{ReadOnly: readOnly, ReadWrite: readWrite}
}

func TestSpreadCostReportsTheRangeOfEveryFigure(t *testing.T) {
	t.Parallel()

	costs := []Cost{
		{Instructions: 100, DiskReadBytes: 10, WriteBytes: 5, ResourceFee: 1000, MinResourceFee: 100},
		{Instructions: 130, DiskReadBytes: 10, WriteBytes: 7, ResourceFee: 1000, MinResourceFee: 100},
		{Instructions: 120, DiskReadBytes: 12, WriteBytes: 6, ResourceFee: 1000, MinResourceFee: 100},
	}

	got := SpreadCost(costs, nil, nil)

	require.Equal(t, 3, got.Runs)
	byName := map[string]FigureSpread{}
	for _, f := range got.Figures {
		byName[f.Name] = f
	}

	cpu := byName["cpu instructions"]
	assert.Equal(t, int64(100), cpu.Min)
	assert.Equal(t, int64(130), cpu.Max)
	assert.Equal(t, int64(30), cpu.Spread)
	require.NotNil(t, cpu.Percent)
	assert.InDelta(t, 30.0, *cpu.Percent, 0.01)

	disk := byName["disk read bytes"]
	assert.Equal(t, int64(10), disk.Min)
	assert.Equal(t, int64(12), disk.Max)

	write := byName["write bytes"]
	assert.Equal(t, int64(5), write.Min)
	assert.Equal(t, int64(7), write.Max)

	// A figure that did not move has no spread.
	fee := byName["resource fee (stroops)"]
	assert.Zero(t, fee.Spread)
}

func TestSpreadCostTakesTheMaximumOfEveryFigure(t *testing.T) {
	t.Parallel()

	costs := []Cost{
		{Instructions: 100, DiskReadBytes: 40, WriteBytes: 5, ResourceFee: 1000, MinResourceFee: 900},
		{Instructions: 130, DiskReadBytes: 10, WriteBytes: 9, ResourceFee: 1200, MinResourceFee: 100},
	}

	got := SpreadCost(costs, nil, nil)

	// The maximum is per figure, not per run: no single run is the worst at
	// everything, and the baseline should still be the worst case.
	assert.Equal(t, uint32(130), got.Max.Instructions)
	assert.Equal(t, uint32(40), got.Max.DiskReadBytes)
	assert.Equal(t, uint32(9), got.Max.WriteBytes)
	assert.Equal(t, int64(1200), got.Max.ResourceFee)
	assert.Equal(t, int64(900), got.Max.MinResourceFee)
}

func TestSpreadCostFlagsNoisyOnlyAboveTheThreshold(t *testing.T) {
	t.Parallel()

	steady := []Cost{{Instructions: 100}, {Instructions: 102}}
	noisy := []Cost{{Instructions: 100}, {Instructions: 120}}

	limit := 5.0
	assert.False(t, SpreadCost(steady, nil, &limit).Noisy, "2%% spread is under a 5%% limit")
	assert.True(t, SpreadCost(noisy, nil, &limit).Noisy, "20%% spread is over a 5%% limit")

	// A nil threshold reports the spread without judging it.
	assert.False(t, SpreadCost(noisy, nil, nil).Noisy)
}

func TestSpreadCostTreatsGrowthFromZeroAsNoisy(t *testing.T) {
	t.Parallel()

	// A figure that was zero and then was not has no percentage spread, and
	// is not a small change, exactly as in a baseline comparison.
	costs := []Cost{{Instructions: 0}, {Instructions: 1}}
	limit := 1000.0

	got := SpreadCost(costs, nil, &limit)

	var cpu FigureSpread
	for _, f := range got.Figures {
		if f.Name == "cpu instructions" {
			cpu = f
		}
	}
	assert.Nil(t, cpu.Percent)
	assert.True(t, got.Noisy)
}

func TestSpreadCostListsFootprintEntriesThatMoved(t *testing.T) {
	t.Parallel()

	costs := []Cost{{}, {}}
	footprints := []Footprint{
		fp([]string{"a", "b"}, []string{"c"}),
		fp([]string{"a"}, []string{"c"}),
	}

	got := SpreadCost(costs, footprints, nil)

	// "b" was read in one run but not the other; "a" and "c" were in both.
	require.Len(t, got.FootprintChanges, 1)
	assert.Contains(t, got.FootprintChanges[0], "b")
}

func TestSpreadCostWithASingleRunHasNoSpread(t *testing.T) {
	t.Parallel()

	got := SpreadCost([]Cost{{Instructions: 100}}, nil, nil)

	assert.Equal(t, 1, got.Runs)
	for _, f := range got.Figures {
		assert.Zero(t, f.Spread)
	}
}

func TestSpreadCostWithoutCostsIsEmpty(t *testing.T) {
	t.Parallel()

	got := SpreadCost(nil, nil, nil)

	assert.Zero(t, got.Runs)
	assert.Empty(t, got.Figures)
}
