//go:build race

package persist

import "time"

// sessionStatsBulkBudget is TestSessionStatsBulkSaveIsFast's wall-clock budget
// for the ~11k-row boundary write. Under -race the budget is loosened, NOT
// because the write is actually slower in production, but because the race
// detector's -d=checkptr instrumentation adds a large, well-documented,
// per-parameter-bind constant-factor tax inside modernc.org/sqlite (a
// cgo-free driver that marshals bound values through unsafe.Pointer
// arithmetic — every one of those conversions gets checkptr-verified under
// -race). Profiling this exact test under -race shows >90% of the time
// inside runtime.checkptrBase/Alignment/Straddles reached from
// modernc.org/sqlite's conn.bind, not inside our batching code — go's own
// race/checkptr tooling overhead, independent of how the ~99k parameter
// values (11k rows x 9 columns) get grouped into batches or transactions.
// The unraced budget (see the !race variant of this file) is the number that
// actually matters for the live boundary writer.
const sessionStatsBulkBudget = 15 * time.Second
