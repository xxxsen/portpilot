package main

import "testing"

func TestVerifyResourceRecovery(t *testing.T) {
	t.Parallel()
	initial := recoverySummary{
		Goroutines:     20,
		OpenFDs:        12,
		HeapInuseBytes: 8 << 20,
	}
	withinLimit := initial
	withinLimit.Goroutines += goroutineRecoveryAllowance
	withinLimit.OpenFDs += fdRecoveryAllowance
	withinLimit.HeapInuseBytes += heapRecoveryAllowance
	if err := verifyResourceRecovery(initial, withinLimit); err != nil {
		t.Fatalf("bounded resource growth was rejected: %v", err)
	}
	leaked := withinLimit
	leaked.Goroutines++
	leaked.OpenFDs++
	leaked.HeapInuseBytes++
	if err := verifyResourceRecovery(initial, leaked); err == nil {
		t.Fatal("resource leak was accepted")
	}
}
