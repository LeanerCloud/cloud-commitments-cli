package main

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkRI(resourceType string, count int) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceRDS, Region: "eu-west-1", ResourceType: resourceType,
		Count: count, CommitmentType: common.CommitmentReservedInstance, EstimatedSavings: 100,
	}
}

func mkSP() common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceCompute, Region: "eu-west-1", ResourceType: "Compute",
		Count: 1, CommitmentType: common.CommitmentSavingsPlan, EstimatedSavings: 100,
	}
}

func TestApplyInstanceLimit_SavingsPlanExempt(t *testing.T) {
	got := ApplyInstanceLimit([]common.Recommendation{mkRI("db.r5.large", 3), mkSP(), mkRI("db.r5.xlarge", 2)}, 1)

	require.Len(t, got, 2)
	assert.Equal(t, 1, got[0].Count)
	assert.Equal(t, common.CommitmentSavingsPlan, got[1].CommitmentType)
	assert.Equal(t, 1, got[1].Count)
}

func TestApplyGlobalInstanceLimit_SavingsPlanExempt(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		recs     []common.Recommendation
		wantRIs  []int
		wantSP   bool
		wantSize int
	}{
		{"cap binds on RIs only", Config{MaxInstances: 1}, []common.Recommendation{mkRI("a", 3), mkSP(), mkRI("b", 2)}, []int{1}, true, 2},
		{"SP alone does not bind the cap", Config{MaxInstances: 1}, []common.Recommendation{mkRI("a", 1), mkSP()}, []int{1}, true, 2},
		{"SP survives cap with min-count above 1", Config{MaxInstances: 1, MinCount: 2}, []common.Recommendation{mkRI("a", 3), mkSP()}, nil, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyGlobalInstanceLimit(tt.recs, tt.cfg, rankBySavingsPercentage, nil)

			var ris []int
			sp := false
			for _, r := range got {
				if isSavingsPlan(r) {
					sp = true
					assert.Equal(t, 1, r.Count)
				} else {
					ris = append(ris, r.Count)
				}
			}
			assert.Equal(t, tt.wantRIs, ris)
			assertSameRelativeOrder(t, tt.recs, got)
			assert.Equal(t, tt.wantSP, sp)
			assert.Len(t, got, tt.wantSize)
		})
	}
}

func TestApplyMinCountFloor_SavingsPlanExempt(t *testing.T) {
	got := applyMinCountFloor([]common.Recommendation{mkRI("a", 1), mkSP(), mkRI("b", 5)}, 3)

	require.Len(t, got, 2)
	assert.Equal(t, []string{"Compute", "b"}, []string{got[0].ResourceType, got[1].ResourceType}, "input order kept")
	var sps, ris int
	for _, r := range got {
		if isSavingsPlan(r) {
			sps++
		} else {
			ris++
			assert.Equal(t, 5, r.Count)
		}
	}
	assert.Equal(t, 1, sps)
	assert.Equal(t, 1, ris)
}

// assertSameRelativeOrder checks got is a subsequence of in by ResourceType.
func assertSameRelativeOrder(t *testing.T, in, got []common.Recommendation) {
	t.Helper()
	i := 0
	for _, g := range got {
		for i < len(in) && in[i].ResourceType != g.ResourceType {
			i++
		}
		require.Less(t, i, len(in), "output order differs from input order")
		i++
	}
}

// scoreLimitAndDisplay hands MinCount to scorer.Score, which must exempt
// Savings Plans (Count 1 is a placeholder) at the pinned library version.
func TestScoreLimitAndDisplay_MinCountKeepsSavingsPlan(t *testing.T) {
	result := scoreLimitAndDisplay([]common.Recommendation{mkSP(), mkRI("a", 3)}, Config{MinCount: 2}, nil)

	types := make([]common.CommitmentType, 0, len(result.Passed))
	for _, r := range result.Passed {
		types = append(types, r.CommitmentType)
	}
	assert.ElementsMatch(t, []common.CommitmentType{common.CommitmentSavingsPlan, common.CommitmentReservedInstance}, types)
}

func TestApplyInstanceLimit_SavingsPlanAfterBudgetSpent(t *testing.T) {
	got := ApplyInstanceLimit([]common.Recommendation{mkRI("a", 3), mkRI("b", 2), mkSP()}, 3)

	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ResourceType)
	assert.True(t, isSavingsPlan(got[1]))
}

func mkNamedSP(name string) common.Recommendation {
	r := mkSP()
	r.ResourceType = name
	return r
}

// mixedOrderInput interleaves Savings Plans with instance rows.
func mixedOrderInput() []common.Recommendation {
	return []common.Recommendation{
		mkNamedSP("S1"), mkRI("a", 3), mkNamedSP("S2"), mkRI("b", 2),
		mkNamedSP("S3"), mkRI("c", 5), mkNamedSP("S4"),
	}
}

func resourceTypes(recs []common.Recommendation) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.ResourceType)
	}
	return out
}

func TestMixedSavingsPlanOrder_Exact(t *testing.T) {
	t.Run("cap 4", func(t *testing.T) {
		got := applyGlobalInstanceLimit(mixedOrderInput(), Config{MaxInstances: 4}, rankBySavingsPercentage, nil)
		assert.Equal(t, []string{"S1", "a", "S2", "b", "S3", "S4"}, resourceTypes(got))
	})
	t.Run("cap 4 min-count 2", func(t *testing.T) {
		got := applyGlobalInstanceLimit(mixedOrderInput(), Config{MaxInstances: 4, MinCount: 2}, rankBySavingsPercentage, nil)
		assert.Equal(t, []string{"S1", "a", "S2", "S3", "S4"}, resourceTypes(got))
	})
	t.Run("min-count floor 3", func(t *testing.T) {
		got := applyMinCountFloor(mixedOrderInput(), 3)
		assert.Equal(t, []string{"S1", "a", "S2", "S3", "c", "S4"}, resourceTypes(got))
	})
}
