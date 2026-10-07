//go:build !linux

package worker

func ensureOpenAIEgressLoopback() error { return nil }
