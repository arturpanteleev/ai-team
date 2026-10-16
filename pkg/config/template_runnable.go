package config

import (
	"fmt"
	"strings"
)

// TrustedConfigError says that a stored template version may not run: it
// carries a command gate (external_program, docker_program) or an
// infrastructure setting the trusted file configuration (.ai-team/config.yaml
// as a person last wrote it) does not contain verbatim. Such a version could
// have been published through the web before the trusted-config rule; running
// it would execute a command nobody reviewed as code.
type TrustedConfigError struct {
	// Subject names what was refused, for example "закреплённый шаблон run X".
	Subject  string
	Problems []string
	// WebWritten is true when the active config.yaml itself still holds bytes the
	// web wrote (so it is not a trusted baseline).
	WebWritten bool
}

// Code is the machine-readable error code (ErrCodeTrustedConfigOnly).
func (e *TrustedConfigError) Code() string { return ErrCodeTrustedConfigOnly }

func (e *TrustedConfigError) Error() string {
	way := "Приведите .ai-team/config.yaml к значениям закреплённого шаблона (правка через review), " +
		"либо запустите новую задачу по активному шаблону"
	if e.WebWritten {
		way = ".ai-team/config.yaml записан через веб и не считается проверенным кодом: откройте файл, " +
			"проверьте его и сохраните любую правку (хеш изменится, и файл снова станет доверенным), " +
			"либо верните командные гейты и инфраструктуру из последней проверенной версии; новую задачу тоже можно запустить после этого"
	}
	return fmt.Sprintf("%s: запуск отклонён (%s): шаблон содержит командные гейты или инфраструктурные настройки, "+
		"которых нет в файловом конфиге .ai-team/config.yaml: %s. %s",
		e.Subject, ErrCodeTrustedConfigOnly, strings.Join(e.Problems, "; "), way)
}

// RequireTrusted refuses data, a template about to be run (a pin, a stored
// version), when it carries commands or infrastructure the trusted file
// configuration does not. Process differences and timeout/model/effort tuning
// are fine (see tuningField), and so is a command gate that is semantically the
// same as one of the same stage of the file configuration.
//
// The trusted file configuration is the active config.yaml unless the web wrote
// it (template_provenance.go): then it is the last file a person wrote, so a
// web publication cannot vouch for itself. Without a file configuration the
// trusted baseline is empty: a template with commands or infrastructure is
// refused, one without them runs.
func (s *TemplateStore) RequireTrusted(data []byte, subject string) error {
	anchor, err := s.anchor()
	if err != nil {
		return fmt.Errorf("%s: %w", subject, err)
	}
	if anchor.hasActive && !anchor.webWritten && string(anchor.active) == string(data) {
		return nil
	}
	submitted, err := ParseYAML(data)
	if err != nil {
		return fmt.Errorf("%s: %w", subject, err)
	}
	if problems := submitted.untrustedRunProblems(anchor.baseline); len(problems) > 0 {
		return &TrustedConfigError{Subject: subject, Problems: problems, WebWritten: anchor.webWritten}
	}
	return nil
}

// ReadRunnablePinnedRun is ReadPinnedRun for code that is about to execute the
// template: a pin that fails RequireTrusted is refused with *TrustedConfigError.
func (s *TemplateStore) ReadRunnablePinnedRun(runID string) ([]byte, string, bool, error) {
	data, version, found, err := s.ReadPinnedRun(runID)
	if err != nil || !found {
		return data, version, found, err
	}
	if err := s.RequireTrusted(data, "закреплённый шаблон run "+runID); err != nil {
		return nil, "", false, err
	}
	return data, version, true, nil
}
