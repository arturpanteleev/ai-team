// Package approvaltest gives tests a filesystem approval store that vouches
// for controller decisions the way the SQLite store does. The production file
// store deliberately does not (its JSON files can be edited outside the
// process), so a test that drives a gate override or a controller-approved
// delivery through the filesystem must opt in explicitly. Never import it
// from non-test code.
package approvaltest

import "github.com/arturpanteleev/ai-team/pkg/approval"

// Store is the filesystem approval store plus the trusted-decision check.
type Store struct{ *approval.Store }

// New opens the filesystem approval store of targetDir.
func New(targetDir string) (*Store, error) {
	inner, err := approval.NewStore(targetDir)
	if err != nil {
		return nil, err
	}
	return &Store{Store: inner}, nil
}

// HasAuthenticatedControllerDecision mirrors the SQLite store: every decision
// of a resolved approval must carry controller provenance for the exact
// approval, subject and action.
func (*Store) HasAuthenticatedControllerDecision(value approval.PendingApproval) bool {
	if value.Status != approval.StatusResolved || len(value.Decisions) == 0 {
		return false
	}
	for _, decision := range value.Decisions {
		if !decision.ControllerAuthenticated || decision.ApprovalID != value.ID ||
			decision.SubjectHash != value.SubjectHash || decision.Action != value.ResolvedAction {
			return false
		}
	}
	return true
}

var _ approval.TrustedDecisionAuthority = (*Store)(nil)
