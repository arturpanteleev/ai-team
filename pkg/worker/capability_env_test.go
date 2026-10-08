package worker

import (
	"os"
	"testing"
)

func TestClearWorkerProcessCapabilities(t *testing.T) {
	names := []string{WorkerAPIAddressEnv, WorkerAPISocketEnv, WorkerAPITokenEnv, OpenAIEgressSocketEnv, OpenAIEgressTokenEnv}
	for _, name := range names {
		t.Setenv(name, "scoped-secret")
	}
	if err := ClearWorkerProcessCapabilities(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, exists := os.LookupEnv(name); exists {
			t.Errorf("worker capability %s remained in process environment", name)
		}
	}
}
