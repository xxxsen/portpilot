package main

import "testing"

func TestSourceIPPrecedes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		items  []analyticsTopIP
		first  string
		second string
		want   bool
	}{
		{
			name:   "permits an unrelated global leader",
			items:  topIPs("127.0.0.1", "127.0.0.2", "127.0.0.3"),
			first:  "127.0.0.2",
			second: "127.0.0.3",
			want:   true,
		},
		{
			name:   "rejects reversed relative order",
			items:  topIPs("127.0.0.1", "127.0.0.3", "127.0.0.2"),
			first:  "127.0.0.2",
			second: "127.0.0.3",
			want:   false,
		},
		{
			name:   "rejects a missing source",
			items:  topIPs("127.0.0.2"),
			first:  "127.0.0.2",
			second: "127.0.0.3",
			want:   false,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := sourceIPPrecedes(test.items, test.first, test.second); got != test.want {
				t.Fatalf("sourceIPPrecedes() = %t, want %t", got, test.want)
			}
		})
	}
}

func topIPs(sourceIPs ...string) []analyticsTopIP {
	items := make([]analyticsTopIP, 0, len(sourceIPs))
	for _, sourceIP := range sourceIPs {
		items = append(items, analyticsTopIP{SourceIP: sourceIP})
	}
	return items
}
