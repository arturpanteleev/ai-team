package pipeline

import "github.com/arturpanteleev/ai-team/pkg/evidence"

// EvidenceStore is a file-shaped persistence seam used by pipeline execution.
// Consumers still rely on RunDir/LogDir paths and some package-level file
// readers, so this is not yet a controller-owned or remote storage contract.
// The default implementation remains evidence.Store on the target filesystem.
// Injecting this port does not move artifacts or evidence out of that
// filesystem and does not isolate workers from controller-owned state.
type EvidenceStore interface {
	RunID() string
	RunDir() string
	LogDir() string
	NewAttemptID(stage string, ordinal int) string
	Append(evidence.Event) error
	SnapshotInputs(attemptID string, inputs []evidence.Artifact) ([]evidence.Artifact, func(), error)
	PublishAttempt(evidence.AttemptManifest, string, []evidence.Artifact, []evidence.Artifact) error
	PublishReportTree(name, source string) error
}

// EvidenceStoreFactory owns opening and creating run evidence for a pipeline.
// Its root argument is deliberately filesystem-shaped for the current local
// implementation; Resume returns evidence.Resume's verified replay data.
type EvidenceStoreFactory interface {
	Start(root string, manifest evidence.RunManifest) (EvidenceStore, error)
	Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error)
}

type filesystemEvidenceStoreFactory struct{}

func (filesystemEvidenceStoreFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	return evidence.Start(root, manifest)
}

func (filesystemEvidenceStoreFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	return evidence.Resume(root, runID)
}
