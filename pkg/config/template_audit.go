package config

import (
	"errors"
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
		// An explicit re-save is the trusted file anchor for these exact bytes;
		// it is no longer a finding. An unreadable anchor is a finding, not a
		// silent pass, so a broken registry cannot hide an unreviewed version.
		anchored, anchorErr := s.isTrustedVersion(data)
		if anchorErr != nil {
			findings = append(findings, TemplateVersionAudit{Version: id, Problems: []string{fmt.Sprintf("якорь доверия не прочитан: %v", anchorErr)}})
			return
		}
		if anchored {
			return
		}
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
	// A publication made before the web-written registry existed left neither a
	// registry record nor a baseline, so the active file trusts itself. It is
	// still a stored immutable version: when its bytes were published (they are
	// in versions/) and it carries commands, list it as a candidate for review.
	// A hand-edited file is not in versions/ and stays out of the audit.
	if !anchor.webWritten && !audited[activeVersion] && len(anchor.baseline.UntrustedCommandProblems(nil)) > 0 {
		published := false
		for _, version := range versions {
			if version.ID == activeVersion {
				published = true
				break
			}
		}
		anchored, anchorErr := s.isTrustedVersion(anchor.active)
		if anchorErr == nil && published && !anchored {
			problems := []string{"активный config.yaml содержит команды или инфраструктуру, но не имеет якоря доверия (возможно, веб-запись до появления реестра)"}
			problems = append(problems, anchor.baseline.UntrustedCommandProblems(nil)...)
			findings = append(findings, TemplateVersionAudit{Version: activeVersion, Problems: problems})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Version < findings[j].Version })
	return findings, nil
}

// versionData resolves a version by its content id. Stored published versions
// live under versions/; an active configuration that predates the store (or a
// pre-registry web write) may only exist as .ai-team/config.yaml.
func (s *TemplateStore) versionData(id string) ([]byte, error) {
	data, err := s.readVersion(id)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	active, _, readErr := s.ReadCurrent()
	if readErr != nil || TemplateVersionID(active) != id {
		return nil, err
	}
	return active, nil
}

// RewriteVersion is the explicit re-save of one reviewed template version. It
// re-writes the exact, digest-verified bytes through the trusted local file
// path and records the trusted file anchor that lets this one version run.
//
// It deliberately does not publish, does not mark the bytes web-written, does
// not save a baseline and does not touch .ai-team/config.yaml. Only the named
// version gains trust; every other version stays subject to RequireTrusted.
func (s *TemplateStore) RewriteVersion(id string) error {
	if !templateVersionPattern.MatchString(id) {
		return fmt.Errorf("недопустимый идентификатор версии шаблона %q", id)
	}
	data, err := s.versionData(id)
	if err != nil {
		return fmt.Errorf("версия %s: %w", id, err)
	}
	if TemplateVersionID(data) != id {
		return fmt.Errorf("версия %s повреждена", id)
	}
	if err := s.writeImmutableVersion(id, data); err != nil {
		return fmt.Errorf("пересохранить версию %s: %w", id, err)
	}
	if err := s.markTrustedVersion(data); err != nil {
		return fmt.Errorf("якорь доверия версии %s: %w", id, err)
	}
	return nil
}
