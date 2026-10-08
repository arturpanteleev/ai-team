package pipeline

import (
	"errors"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
)

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
	SnapshotInputs(attemptID string, inputs []evidence.Artifact) ([]evidence.Artifact, func() error, error)
	PublishAttempt(evidence.AttemptManifest, string, []evidence.Artifact, []evidence.Artifact) error
	PublishReportTree(name, source string) error
	SealTerminalEvidence() error
}

// EvidenceStoreFactory owns opening and creating run evidence for a pipeline.
// Its root argument is deliberately filesystem-shaped for the current local
// implementation; Resume returns evidence.Resume's verified replay data.
type EvidenceStoreFactory interface {
	Start(root string, manifest evidence.RunManifest) (EvidenceStore, error)
	Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error)
}

type filesystemEvidenceStoreFactory struct{}

type attemptManifestResumeFactory interface {
	ResumeWithAttemptManifestSource(root, runID string, source evidence.AttemptManifestSource) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error)
}

type eventLogStoreFactory interface {
	StartWithEventLog(root string, manifest evidence.RunManifest, source evidence.EventLog) (EvidenceStore, error)
	ResumeWithEventLog(root, runID string, eventSource evidence.EventLog, manifestSource evidence.AttemptManifestSource) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error)
}

func (filesystemEvidenceStoreFactory) Start(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	return evidence.Start(root, manifest)
}

func (filesystemEvidenceStoreFactory) StartWithEventLog(root string, manifest evidence.RunManifest, source evidence.EventLog) (EvidenceStore, error) {
	return evidence.StartWithEventLog(root, manifest, source)
}

func (filesystemEvidenceStoreFactory) Resume(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	return evidence.Resume(root, runID)
}

func (filesystemEvidenceStoreFactory) ResumeWithAttemptManifestSource(root, runID string, source evidence.AttemptManifestSource) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	return evidence.ResumeWithAttemptManifestSource(root, runID, source)
}

func (filesystemEvidenceStoreFactory) ResumeWithEventLog(root, runID string, eventSource evidence.EventLog, manifestSource evidence.AttemptManifestSource) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	return evidence.ResumeWithEventLog(root, runID, eventSource, manifestSource)
}

func (p *Pipeline) startEvidence(root string, manifest evidence.RunManifest) (EvidenceStore, error) {
	if p.eventLogSource == nil {
		return p.evidence.Start(root, manifest)
	}
	factory, ok := p.evidence.(eventLogStoreFactory)
	if !ok {
		return nil, errors.New("configured evidence factory does not support event log sources")
	}
	return factory.StartWithEventLog(root, manifest, p.eventLogSource)
}

func (p *Pipeline) resumeEvidence(root, runID string) (EvidenceStore, evidence.RunManifest, evidence.ReplayedRun, error) {
	if p.eventLogSource != nil {
		factory, ok := p.evidence.(eventLogStoreFactory)
		if !ok {
			return nil, evidence.RunManifest{}, evidence.ReplayedRun{}, errors.New("configured evidence factory does not support event log sources")
		}
		return factory.ResumeWithEventLog(root, runID, p.eventLogSource, p.attemptManifestSource)
	}
	if factory, ok := p.evidence.(attemptManifestResumeFactory); ok {
		return factory.ResumeWithAttemptManifestSource(root, runID, p.attemptManifestSource)
	}
	return p.evidence.Resume(root, runID)
}
