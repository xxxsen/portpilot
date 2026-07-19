package main

import (
	"errors"
	"fmt"
)

const (
	goroutineRecoveryAllowance = 32
	fdRecoveryAllowance        = 16
	heapRecoveryAllowance      = 64 << 20
)

func verifyResourceRecovery(initial, final recoverySummary) error {
	var problems []error
	if final.Goroutines > initial.Goroutines+goroutineRecoveryAllowance {
		problems = append(problems, fmt.Errorf(
			"goroutines did not recover: initial=%d final=%d",
			initial.Goroutines,
			final.Goroutines,
		))
	}
	if initial.OpenFDs >= 0 && final.OpenFDs > initial.OpenFDs+fdRecoveryAllowance {
		problems = append(problems, fmt.Errorf(
			"file descriptors did not recover: initial=%d final=%d",
			initial.OpenFDs,
			final.OpenFDs,
		))
	}
	if final.HeapInuseBytes > initial.HeapInuseBytes+heapRecoveryAllowance {
		problems = append(problems, fmt.Errorf(
			"heap did not recover: initial=%d final=%d",
			initial.HeapInuseBytes,
			final.HeapInuseBytes,
		))
	}
	return errors.Join(problems...)
}
