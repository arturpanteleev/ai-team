package worker

import "os"

// ClearWorkerProcessCapabilities removes credentials that are needed only to
// initialize the worker process itself. Runtime, tools, git, and gh children
// must not inherit these ambient capabilities.
func ClearWorkerProcessCapabilities() error {
	for _, name := range []string{
		workerAPIAddressEnv, workerAPISocketEnv, workerAPITokenEnv,
		openAIEgressSocketEnv, openAIEgressTokenEnv,
	} {
		if err := os.Unsetenv(name); err != nil {
			return err
		}
	}
	return nil
}
