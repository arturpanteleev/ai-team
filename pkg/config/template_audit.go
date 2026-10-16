package config

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

func (s *TemplateStore) readVersion(id string) ([]byte, error) {
	return safeio.ReadRegularFile(filepath.Join(s.target, ".ai-team", "templates", "versions", id+".yaml"), MaxTemplateYAMLBytes)
}

// TemplateVersionAudit lists why one stored template version differs from the
// active file configuration in commands or infrastructure.
type TemplateVersionAudit struct {
	Version  string
	Problems []string
}

// AuditVersions inspects the stored template versions (.ai-team/templates/versions)
// against the active .ai-team/config.yaml and reports every version that
// carries a command gate, an executor adapter, a check, an MCP server or an
// infrastructure section the active template does not contain verbatim.
//
// Versions published through the web before the trusted-config rule could hold
// such definitions. The store records no provenance, so this is an audit for
// the operator, not an enforcement point: the active template is the baseline
// and is itself never flagged. Versions that do not parse are skipped.
func (s *TemplateStore) AuditVersions() ([]TemplateVersionAudit, error) {
	activeData, activeVersion, err := s.ReadCurrent()
	if err != nil {
		return nil, fmt.Errorf("активный шаблон: %w", err)
	}
	active, err := ParseYAML(activeData)
	if err != nil {
		return nil, fmt.Errorf("активный шаблон: %w", err)
	}
	versions, err := s.ListVersions()
	if err != nil {
		return nil, err
	}
	var findings []TemplateVersionAudit
	for _, version := range versions {
		if version.ID == activeVersion {
			continue
		}
		data, readErr := s.readVersion(version.ID)
		if readErr != nil {
			continue
		}
		parsed, parseErr := ParseYAML(data)
		if parseErr != nil {
			continue
		}
		if problems := parsed.UntrustedCommandProblems(active); len(problems) > 0 {
			findings = append(findings, TemplateVersionAudit{Version: version.ID, Problems: problems})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Version < findings[j].Version })
	return findings, nil
}
