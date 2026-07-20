package agentdata

import "embed"

//go:embed all:agents
var Agents embed.FS

// Frontend contains the production web bundle so an installed ai-team binary
// does not depend on the source checkout being present at runtime.
//
//go:embed all:web/dist
var Frontend embed.FS
