package approval

import (
	"path/filepath"
	"testing"
)

func TestIsTrustedControllerDecisionFailsClosedForStoresWithoutTheCapability(t *testing.T) {
	file, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value := PendingApproval{ID: "a", SubjectHash: "h", Status: StatusResolved, ResolvedAction: "approve",
		Decisions: []Decision{{ApprovalID: "a", SubjectHash: "h", Action: "approve", ControllerAuthenticated: true}}}
	if IsTrustedControllerDecision(file, value) {
		t.Fatal("the filesystem store must never vouch for a controller decision")
	}
	if IsTrustedControllerDecision(nil, value) {
		t.Fatal("a nil store must not vouch")
	}
	sqlite, err := NewSQLiteStore(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	if !IsTrustedControllerDecision(sqlite, value) {
		t.Fatal("the SQLite store vouches for an authenticated controller decision")
	}
	value.Decisions[0].ControllerAuthenticated = false
	if IsTrustedControllerDecision(sqlite, value) {
		t.Fatal("a decision without controller provenance must not be trusted")
	}
}
