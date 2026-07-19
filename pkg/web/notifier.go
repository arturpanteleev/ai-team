package web

import (
	"context"
	"encoding/json"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

type WebNotifier struct {
	store *store.Store
	hub   *Hub
}

func NewWebNotifier(s *store.Store, h *Hub) *WebNotifier {
	return &WebNotifier{
		store: s,
		hub:   h,
	}
}

func (wn *WebNotifier) Notify(ctx context.Context, stage notifier.StageResult) error {
	// Find or create stage in store
	stages, err := wn.store.GetStagesByPipelineRunID(0) // This is a simplified approach
	if err != nil {
		return err
	}

	// Find the stage for this agent
	var targetStage *store.Stage
	for _, s := range stages {
		if s.AgentName == stage.Name {
			targetStage = &s
			break
		}
	}

	if targetStage == nil {
		// Create new stage
		targetStage = &store.Stage{
			PipelineRunID: 0, // Will be set by pipeline integration
			AgentName:     stage.Name,
			Status:        stage.Status,
			StartedAt:     time.Now().Add(-stage.Duration),
		}
		if err := wn.store.CreateStage(targetStage); err != nil {
			return err
		}
	}

	// Update stage
	now := time.Now()
	targetStage.Status = stage.Status
	targetStage.CompletedAt = &now
	targetStage.DurationMs = stage.Duration.Milliseconds()

	if stage.Err != nil {
		targetStage.Error = stage.Err.Error()
	}

	if stage.Inputs != nil {
		inputsJSON, _ := json.Marshal(stage.Inputs)
		targetStage.InputsJSON = string(inputsJSON)
	}

	if stage.Outputs != nil {
		outputsJSON, _ := json.Marshal(stage.Outputs)
		targetStage.OutputsJSON = string(outputsJSON)
	}

	if err := wn.store.UpdateStage(targetStage); err != nil {
		return err
	}

	// Broadcast WebSocket event
	wn.hub.BroadcastEvent(Event{
		Type:       "stage_completed",
		PipelineID: targetStage.PipelineRunID,
		Agent:      stage.Name,
		Status:     stage.Status,
		DurationMs: stage.Duration.Milliseconds(),
	})

	return nil
}

func (wn *WebNotifier) NotifyStageStarted(pipelineID int64, agentName string) {
	wn.hub.BroadcastEvent(Event{
		Type:       "stage_started",
		PipelineID: pipelineID,
		Agent:      agentName,
	})
}

func (wn *WebNotifier) NotifyPipelineCompleted(pipelineID int64, status string) {
	wn.hub.BroadcastEvent(Event{
		Type:       "pipeline_completed",
		PipelineID: pipelineID,
		Status:     status,
	})
}
