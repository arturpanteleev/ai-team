package web

import (
	"fmt"
	"os"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

// reconcileOrphanRuns turns runs left in "running" by a process that has since
// exited into "interrupted", so the operator can continue them. The workspace
// lock is the liveness signal: if another process holds it, a live CLI run owns
// the target and nothing is touched.
func (s *Server) reconcileOrphanRuns(now time.Time) {
	if s.targetDir == "" || s.store == nil {
		return
	}
	lock, err := evidence.AcquireWorkspaceLock(s.targetDir)
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }() // снятие блокировки на выходе: ошибку обработать негде.
	if err := s.store.ReconcileInterrupted(now); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ web store (reconcile orphan runs): %v\n", err)
	}
}
