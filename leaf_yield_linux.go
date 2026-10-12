package wormhole

import "syscall"

// Briefly yield this OS thread without parking the goroutine on its locked M.
// leafLockWait still yields to the Go scheduler after a bounded number of
// attempts, and immediately on one P, so oversubscribed callers can progress.
func leafOSYield() { syscall.RawSyscall(syscall.SYS_SCHED_YIELD, 0, 0, 0) }
