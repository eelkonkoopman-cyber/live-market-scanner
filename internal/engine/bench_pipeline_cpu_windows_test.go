package engine

import "syscall"

// pipeCPUSeconds is the user+kernel CPU time this process has used, in seconds.
func pipeCPUSeconds() float64 {
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(self, &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	ticks := func(ft syscall.Filetime) uint64 { return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime) }
	return float64(ticks(kernel)+ticks(user)) * 1e-7 // 100 ns units
}
