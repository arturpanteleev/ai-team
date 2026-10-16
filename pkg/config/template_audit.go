package config

import (
	"fmt"
	"os"
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
// and the active template against the trusted file configuration and reports
// every one that carries a command gate, an executor adapter, a check, an MCP
// server or an infrastructure section the trusted configuration does not
// contain verbatim.
//
// The trusted configuration is the active config.yaml while a person wrote it.
// When the web wrote it (template_provenance.go) the baseline is the last file
// a person wrote, and the active template is audited too, so a web publication
// cannot vouch for itself. Versions published through the web before the
// trusted-config rule could hold such definitions; the store records no
// provenance for them, so the audit is for the operator, and running such a
// version is refused separately (RequireTrusted). A version that cannot be read
// or parsed is reported as a finding, not skipped.
func (s *TemplateStore) AuditVersions() ([]TemplateVersionAudit, error) {
	anchor, err := s.anchor()
	if err != nil {
		return nil, fmt.Errorf("активный шаблон: %w", err)
	}
	if !anchor.hasActive {
		return nil, fmt.Errorf("активный шаблон: %w", os.ErrNotExist)
	}
	activeVersion := TemplateVersionID(anchor.active)
	versions, err := s.ListVersions()
	if err != nil {
		return nil, err
	}
	var findings []TemplateVersionAudit
	audited := map[string]bool{}
	audit := func(id string, data []byte) {
		audited[id] = true
		parsed, parseErr := ParseYAML(data)
		if parseErr != nil {
			findings = append(findings, TemplateVersionAudit{Version: id, Problems: []string{fmt.Sprintf("версия не разбирается: %v", parseErr)}})
			return
		}
		problems := parsed.UntrustedCommandProblems(anchor.baseline)
		if len(problems) > 0 && id == activeVersion {
			problems = append([]string{"активный config.yaml записан через веб и не редактировался вручную"}, problems...)
		}
		if len(problems) > 0 {
			findings = append(findings, TemplateVersionAudit{Version: id, Problems: problems})
		}
	}
	for _, version := range versions {
		if version.ID == activeVersion && !anchor.webWritten {
			continue
		}
		data, readErr := s.readVersion(version.ID)
		if readErr != nil {
			findings = append(findings, TemplateVersionAudit{Version: version.ID, Problems: []string{fmt.Sprintf("версию не удалось прочитать: %v", readErr)}})
			continue
		}
		audit(version.ID, data)
	}
	if anchor.webWritten && !audited[activeVersion] {
		audit(activeVersion, anchor.active)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Version < findings[j].Version })
	return findings, nil
}
