//go:build !race

package persist

import "time"

// sessionStatsBulkBudget is TestSessionStatsBulkSaveIsFast's wall-clock budget
// for the ~11k-row boundary write: the real production number (the boundary
// writer goroutine must never visibly lag the engine). See the race-build
// variant of this file for why the budget differs there.
const sessionStatsBulkBudget = time.Second
