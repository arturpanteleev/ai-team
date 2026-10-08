package evidence

import (
	"fmt"

	"github.com/arturpanteleev/ai-team/pkg/candidate"
	"github.com/arturpanteleev/ai-team/pkg/metrics"
)

// otherControllerRunAuthority reports controller-owned run proofs which are
// independent of the event store. They prevent a missing event-store tree
// from turning a cloud run into an apparently local filesystem run.
func otherControllerRunAuthority(target, runID string) (bool, error) {
	attemptsReserved, err := (ControllerAttemptManifestStore{TargetDir: target}).IsReserved(runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller attempt-manifest reservation: %w", err)
	}
	usageReserved, err := metrics.UsageEnvelopeReservation(target, runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller usage reservation: %w", err)
	}
	candidateAdmitted, err := (candidate.FileMetadataStore{}).HasControllerAdmissionProof(target, runID)
	if err != nil {
		return false, fmt.Errorf("inspect controller candidate admission: %w", err)
	}
	return attemptsReserved || usageReserved || candidateAdmitted, nil
}
