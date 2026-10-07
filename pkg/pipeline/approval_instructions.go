package pipeline

import (
	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/logging"
)

// logSQLiteApprovalRoute points cloud/web runs to the authenticated controller
// route. The local `ai-team decision` command intentionally edits only the
// filesystem store and cannot decide rows in the shared web database.
func (rs *runState) logSQLiteApprovalRoute(value approval.PendingApproval) {
	if _, ok := rs.approvalStore.(*approval.SQLiteStore); !ok {
		return
	}
	logging.Printf("Решение внесите через web UI или авторизованный POST /api/runs/%s/approvals/%s/decisions (локальный `ai-team decision` не изменяет web DB).\n",
		value.RunID, value.ID)
}
