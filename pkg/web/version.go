package web

import (
	"net/http"
	"runtime/debug"
)

// buildRevision is the commit this binary was built from. Modified reports
// local changes in the tree at build time, so the UI never claims a clean build
// that it is not.
type buildRevision struct {
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
	Time     string `json:"time,omitempty"`
}

func readBuildRevision() buildRevision {
	var out buildRevision
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			out.Revision = setting.Value
		case "vcs.modified":
			out.Modified = setting.Value == "true"
		case "vcs.time":
			out.Time = setting.Value
		}
	}
	return out
}

// handleVersion reports which backend build is serving the UI. The frontend
// bundle identifies itself separately (the content hash of its entry script).
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSONResponse(w, http.StatusOK, map[string]any{"backend": readBuildRevision()})
}
