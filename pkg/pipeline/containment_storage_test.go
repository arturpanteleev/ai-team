package pipeline

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/containment"
)

type captureContainmentWriter func(containment.Receipt) error

func (f captureContainmentWriter) WriteContainmentReceipt(receipt containment.Receipt) error {
	return f(receipt)
}

func TestContainmentReceiptUsesControllerWriterWhenConfigured(t *testing.T) {
	var written *containment.Receipt
	p := &Pipeline{containmentWriter: captureContainmentWriter(func(receipt containment.Receipt) error {
		copy := receipt
		written = &copy
		return nil
	})}
	rs := &runState{p: p, runCfg: RunConfig{ContainmentProfile: "trusted-local"}}
	if err := rs.writeContainmentReceipt(); err != nil {
		t.Fatalf("write through controller receipt writer: %v", err)
	}
	if written == nil || written.Profile != "trusted-local" || written.Validate() != nil {
		t.Fatalf("unexpected controller receipt: %+v", written)
	}
}
