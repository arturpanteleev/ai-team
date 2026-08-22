package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/pipeline"
	"github.com/arturpanteleev/ai-team/pkg/worker"
)

type JobExecutor interface {
	Execute(context.Context, worker.Job) (pipeline.RunResult, error)
}

type RunArchiver interface {
	Archive(runID string) error
}

type Poller struct {
	queue     *Queue
	executor  JobExecutor
	archiver  RunArchiver
	heartbeat time.Duration
}

func NewPoller(queue *Queue, executor JobExecutor, archiver RunArchiver) (*Poller, error) {
	if queue == nil || executor == nil {
		return nil, errors.New("scheduler poller требует queue и executor")
	}
	heartbeat := queue.options.LeaseDuration / 3
	if heartbeat < 100*time.Millisecond {
		heartbeat = 100 * time.Millisecond
	}
	return &Poller{queue: queue, executor: executor, archiver: archiver, heartbeat: heartbeat}, nil
}

// RunOnce возвращает claimed=false, когда очередь сейчас пуста или ограничена.
func (p *Poller) RunOnce(ctx context.Context, owner string) (claimed bool, err error) {
	record, claimed, err := p.queue.Claim(ctx, owner)
	if err != nil || !claimed {
		return claimed, err
	}
	executionContext, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	heartbeatDone := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	heartbeatError := make(chan error, 1)
	go func() {
		defer close(heartbeatStopped)
		ticker := time.NewTicker(p.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-executionContext.Done():
				return
			case <-ticker.C:
				cancelled, renewErr := p.queue.Renew(
					executionContext, record.ID, owner, record.LeaseToken,
				)
				if renewErr != nil {
					heartbeatError <- renewErr
					cancelExecution()
					return
				}
				if cancelled {
					cancelExecution()
					return
				}
			}
		}
	}()

	_, executionErr := p.executor.Execute(executionContext, record.Job)
	close(heartbeatDone)
	<-heartbeatStopped
	var leaseErr error
	select {
	case leaseErr = <-heartbeatError:
	default:
	}
	if leaseErr != nil {
		return true, leaseErr
	}

	success, diagnostic := classifyExecution(executionErr)
	if p.archiver != nil {
		if archiveErr := p.archiver.Archive(record.Job.RunID); archiveErr != nil {
			success = false
			diagnostic = errors.Join(executionErr, fmt.Errorf("archive run: %w", archiveErr)).Error()
		}
	}
	if completeErr := p.queue.Complete(record.ID, owner, record.LeaseToken, success, diagnostic); completeErr != nil {
		return true, completeErr
	}
	return true, nil
}

// classifyExecution отличает durable business-исходы run (waiting/blocked/
// stopped/canceled/completed) от инфраструктурного сбоя воркера. Решение
// принимает строка результата `ai-team worker`, а не exit code: crash,
// preflight fatal или чужой binary строки не пишут и классифицируются как
// инфраструктурный сбой.
func classifyExecution(err error) (success bool, diagnostic string) {
	if err == nil {
		return true, ""
	}
	diagnostic = err.Error()
	var processError *worker.ProcessError
	if !errors.As(err, &processError) || processError.Result == nil {
		return false, diagnostic
	}
	return processError.Result.Controlled(), diagnostic
}
