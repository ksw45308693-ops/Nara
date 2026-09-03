package app

import "testing"

func TestRegionLookupBudgetReservesHourlyListCalls(t *testing.T) {
	const (
		hourlyRuns       = 24
		maxRetryAttempts = 3
	)
	worstCaseCalls := regionLookupBudgetPerCollection*maxRetryAttempts*hourlyRuns + len(collectionCategories)*hourlyRuns
	if worstCaseCalls > defaultDailyAPICallLimit {
		t.Fatalf("worst-case daily calls = %d, limit = %d", worstCaseCalls, defaultDailyAPICallLimit)
	}
}
