package server

import (
	"testing"

	"github.com/portpilot/portpilot/internal/store"
)

func TestComparisonChangeSemantics(t *testing.T) {
	tests := []struct {
		name              string
		current, previous float64
		state             string
		relative          *float64
	}{
		{"zero", 0, 0, "unchanged", floatPointer(0)},
		{"new", 5, 0, "new", nil},
		{"inactive", 0, 5, "inactive", floatPointer(-1)},
		{"increase", 12, 10, "increased", floatPointer(.2)},
		{"decrease", 5, 10, "decreased", floatPointer(-.5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := changeValue(test.current, test.previous, false)
			if value.State != test.state || value.AbsoluteChange != test.current-test.previous {
				t.Fatalf("change=%#v", value)
			}
			if test.relative == nil {
				if value.RelativeChange != nil {
					t.Fatalf("relative=%v want nil", *value.RelativeChange)
				}
			} else if value.RelativeChange == nil || !closeFloat(*value.RelativeChange, *test.relative) {
				t.Fatalf("relative=%v want=%v", value.RelativeChange, *test.relative)
			}
		})
	}
	rate := rateChange(.2, .1)
	if rate.RelativeChange != nil || !closeFloat(rate.AbsoluteChange, .1) {
		t.Fatalf("failure rate change=%#v", rate)
	}
}

func TestComparisonRankStates(t *testing.T) {
	current := []store.AnalyticsTopIP{
		{SourceIP: "192.0.2.1", Rank: 1},
		{SourceIP: "192.0.2.3", Rank: 2},
		{SourceIP: "192.0.2.4", Rank: 3},
	}
	previous := []store.AnalyticsTopIP{
		{SourceIP: "192.0.2.3", Rank: 1},
		{SourceIP: "192.0.2.1", Rank: 2},
		{SourceIP: "192.0.2.2", Rank: 3},
	}
	values := compareRankings(current, previous, 5)
	status := make(map[string]string)
	for _, value := range values {
		status[value.SourceIP] = value.Status
	}
	want := map[string]string{
		"192.0.2.1": "up", "192.0.2.2": "departed",
		"192.0.2.3": "down", "192.0.2.4": "new",
	}
	for ip, expected := range want {
		if status[ip] != expected {
			t.Fatalf("%s status=%q want=%q, all=%#v", ip, status[ip], expected, values)
		}
	}
}

func TestProxyChangeThresholdBoundariesAndEstimatedTraffic(t *testing.T) {
	previous := []store.AnalyticsDimension{{
		ClientID: "c", ProxyID: "p", Protocol: "tcp",
		Connections: 50, Failures: 0, UpBytes: 5 << 20,
	}}
	current := []store.AnalyticsDimension{{
		ClientID: "c", ProxyID: "p", Protocol: "tcp",
		Connections: 100, Failures: 10, UpBytes: 10 << 20,
	}}
	values := proxyChangeSignals(current, previous, false)
	if len(values) != 1 {
		t.Fatalf("signals=%#v", values)
	}
	got := make(map[string]bool)
	for _, signal := range values[0].Signals {
		got[signal] = true
	}
	for _, signal := range []string{"failure_rate_spike", "connection_spike", "traffic_spike"} {
		if !got[signal] {
			t.Fatalf("missing %s in %#v", signal, values[0].Signals)
		}
	}
	values = proxyChangeSignals(current, previous, true)
	if len(values) != 1 {
		t.Fatalf("estimated signals=%#v", values)
	}
	for _, signal := range values[0].Signals {
		if signal == "traffic_spike" {
			t.Fatalf("estimated traffic produced traffic spike: %#v", values)
		}
	}
	below := current
	below[0].Connections = 99
	below[0].Failures = 9
	below[0].UpBytes = (10 << 20) - 1
	if values := proxyChangeSignals(below, previous, false); len(values) != 0 {
		t.Fatalf("below-boundary signals=%#v", values)
	}
}

func floatPointer(value float64) *float64 { return &value }

func closeFloat(left, right float64) bool {
	difference := left - right
	if difference < 0 {
		difference = -difference
	}
	return difference < 1e-9
}
