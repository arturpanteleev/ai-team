package notifier

import (
	"context"
)

type NotifierChain struct {
	notifiers []Notifier
}

func NewNotifierChain(notifiers ...Notifier) *NotifierChain {
	return &NotifierChain{notifiers: notifiers}
}

func (c *NotifierChain) Notify(ctx context.Context, stage StageResult) error {
	var lastErr error
	for _, n := range c.notifiers {
		if err := n.Notify(ctx, stage); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
