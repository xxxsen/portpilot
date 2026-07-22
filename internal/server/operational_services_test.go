package server

import (
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

func TestAttentionObservationLifecycle(t *testing.T) {
	service := &AttentionService{firstObserved: make(map[string]int64)}
	items := []AttentionItem{
		{ID: "warning:item", Severity: "warning"},
		{ID: "critical:item", Severity: "critical"},
	}
	service.observe(items, 100)
	for _, item := range items {
		if item.FirstObservedAt != 100 || item.UpdatedAt != 100 {
			t.Fatalf("initial observation=%#v", item)
		}
	}
	service.observe(items, 200)
	for _, item := range items {
		if item.FirstObservedAt != 100 || item.UpdatedAt != 200 {
			t.Fatalf("continued observation=%#v", item)
		}
	}

	service.observe(nil, 300)
	fresh := []AttentionItem{{ID: "warning:item", Severity: "warning"}}
	service.observe(fresh, 400)
	if fresh[0].FirstObservedAt != 400 || fresh[0].UpdatedAt != 400 {
		t.Fatalf("re-observation=%#v", fresh[0])
	}
	if severityPriority("critical") >= severityPriority("warning") ||
		severityPriority("warning") >= severityPriority("info") {
		t.Fatal("attention severity priority is not critical, warning, info")
	}
}

func TestSearchRankingDeduplicationAndTypeCaps(t *testing.T) {
	if activityPrefixPattern.MatchString("abcdef1") || !activityPrefixPattern.MatchString("abcdef12") ||
		activityPrefixPattern.MatchString("abcdef1234567890abcdef1234567890a") {
		t.Fatal("activity prefix length classification is invalid")
	}
	if score := searchTextScore("branch", "branch-office", true, false); score != 90 {
		t.Fatalf("ID prefix score=%d", score)
	}
	if score := searchTextScore("office", "Office", false, true); score != 85 {
		t.Fatalf("remark exact score=%d", score)
	}
	if score := searchAddressScore("127.0.0.1", "127.0.0.1:7000"); score != 80 {
		t.Fatalf("address prefix score=%d", score)
	}

	activity := activitySearchItem(store.NetworkConnection{
		ActivityID: "abcdef123456", ClientID: "client-a", ProxyID: "proxy-a",
		Protocol: "tcp", State: "active",
	}, "active", "abcdef12")
	if activity.Score != 90 || activity.Destination.View != "network" ||
		activity.Destination.Params["activity_id"] != "abcdef123456" ||
		activity.Destination.Params["connection_view"] != "active" {
		t.Fatalf("activity search destination=%#v", activity)
	}

	items := []SearchItem{
		{Type: "activity", ID: "abcdef12", Score: 100, Destination: SearchDestination{Params: map[string]string{"connection_view": "history"}}},
		{Type: "activity", ID: "abcdef12", Score: 90, Destination: SearchDestination{Params: map[string]string{"connection_view": "active"}}},
	}
	deduplicated := deduplicateSearchItems(items)
	if len(deduplicated) != 1 || deduplicated[0].Destination.Params["connection_view"] != "active" {
		t.Fatalf("active activity did not win deduplication: %#v", deduplicated)
	}

	many := make([]SearchItem, 0, 8)
	for index := 0; index < 7; index++ {
		many = append(many, SearchItem{Type: "proxy", ID: string(rune('a' + index))})
	}
	many = append(many, SearchItem{Type: "client", ID: "client-a"})
	capped := capSearchTypes(many, 5)
	if len(capped) != 6 {
		t.Fatalf("type-capped results=%d want=6", len(capped))
	}
}

func TestSearchRateLimiterAllowsBurstThenRefills(t *testing.T) {
	limiter := newSearchRateLimiter()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for index := 0; index < 5; index++ {
		if !limiter.allow("session") {
			t.Fatalf("burst request %d rejected", index)
		}
	}
	if limiter.allow("session") {
		t.Fatal("sixth burst request was allowed")
	}
	now = now.Add(2 * time.Second)
	if !limiter.allow("session") {
		t.Fatal("request was not allowed after token refill")
	}
}
