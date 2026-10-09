//go:build !e2etest

package main

import "github.com/arturpanteleev/ai-team/pkg/config"

// e2eInMemoryLegacyConfig is disabled in normal and release builds. The e2etest
// build tag provides an in-memory compatibility fixture for the pre-v5 runtime.
func e2eInMemoryLegacyConfig(string) (*config.Config, bool) { return nil, false }
