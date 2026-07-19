package server

import (
	"os"
	"runtime"
)

type processResources struct {
	Goroutines   int
	OpenFDs      int
	HeapAlloc    uint64
	HeapInuse    uint64
	StackInuse   uint64
	SystemMemory uint64
}

func currentProcessResources() processResources {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return processResources{
		Goroutines:   runtime.NumGoroutine(),
		OpenFDs:      openFileDescriptors(),
		HeapAlloc:    memory.HeapAlloc,
		HeapInuse:    memory.HeapInuse,
		StackInuse:   memory.StackInuse,
		SystemMemory: memory.Sys,
	}
}

func openFileDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
