package gatesched

import (
	"time"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// NewAttempt builds the running attempt record of a gate instance: the n-th
// (1-based) attempt on the wave key. The scheduler uses it for every gate it
// invokes; the controller uses it for gates it records itself (the human
// decision, which is not an invocation of a runner).
func NewAttempt(key WaveKey, ref workflow.GateRef, def workflow.GateDefinition, group, ordinal int, now time.Time) workflow.GateAttempt {
	return workflow.GateAttempt{
		TaskID: key.TaskID, Stage: key.Stage, Visit: key.Visit,
		ArtifactSHA256: key.ArtifactSHA256,
		Gate:           ref.Gate, GateVersion: ref.Version, GateName: ref.InstanceName(),
		Mode:      ref.Mode.Effective(),
		AttemptID: AttemptID(key, ref.InstanceName(), ordinal),
		Executor:  def.Kind, Group: group,
		Execution: workflow.GateExecutionRunning,
		StartedAt: now,
	}
}
