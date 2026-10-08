//go:build !linux

package worker

// ProtectWorkerProcess is a no-op outside Linux; Linux bubblewrap workers use
// PR_SET_DUMPABLE to prevent same-UID child inspection of their initial env.
func ProtectWorkerProcess() error { return nil }
