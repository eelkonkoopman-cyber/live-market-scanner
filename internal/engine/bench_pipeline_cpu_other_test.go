//go:build !windows

package engine

// pipeCPUSeconds: process CPU time is only read on Windows (the benchmark then
// omits its cpu-ms/op metric elsewhere).
func pipeCPUSeconds() float64 { return 0 }
