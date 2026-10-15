package web

import (
	"net/http"

	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// gateCatalogEntry is one published gate definition as the catalog shows it.
// Digest lets a client verify that a template pin matches the definition.
type gateCatalogEntry struct {
	workflow.GateDefinition
	Digest string `json:"digest"`
	// Latest marks the highest published version of the ID (an authoring
	// hint; templates still pin an exact version).
	Latest bool `json:"latest"`
}

// handleGateCatalog serves the read-only catalog of the versioned gate
// library (GET /api/gates/catalog). The library lives in Go code and changes
// only through a PR: there is deliberately no write route.
func (s *Server) handleGateCatalog(w http.ResponseWriter, _ *http.Request) {
	library := gatelib.Default()
	definitions := library.Catalog()
	entries := make([]gateCatalogEntry, 0, len(definitions))
	for _, def := range definitions {
		latest, _ := library.Latest(def.ID)
		entries = append(entries, gateCatalogEntry{GateDefinition: def, Digest: gatelib.Digest(def), Latest: latest.Version == def.Version})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, map[string]any{"gates": entries, "kinds": workflow.GateKinds})
}
