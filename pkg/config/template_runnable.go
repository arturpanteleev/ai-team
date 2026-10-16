package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// TrustedConfigError says that a stored template version may not run: it
// carries a command gate (external_program, docker_program) or an
// infrastructure setting the repository's file configuration
// (.ai-team/config.yaml) does not contain verbatim. Such a version could have
// been published through the web before the trusted-config rule; running it
// would execute a command nobody reviewed as code.
type TrustedConfigError struct {
	// Subject names what was refused, for example "закреплённый шаблон run X".
	Subject  string
	Problems []string
}

// Code is the machine-readable error code (ErrCodeTrustedConfigOnly).
func (e *TrustedConfigError) Code() string { return ErrCodeTrustedConfigOnly }

func (e *TrustedConfigError) Error() string {
	return fmt.Sprintf("%s: запуск отклонён (%s): шаблон содержит командные гейты или инфраструктурные настройки, "+
		"которых нет в файловом конфиге .ai-team/config.yaml: %s. "+
		"Перенесите их в .ai-team/config.yaml через review либо запустите новую задачу по активному шаблону",
		e.Subject, ErrCodeTrustedConfigOnly, strings.Join(e.Problems, "; "))
}

// RequireTrusted refuses data, a template about to be run (a pin, a stored
// version), when it differs from the active file configuration in commands or
// infrastructure. Process differences and timeout/model/effort tuning are
// fine (see tuningField). The active template itself
// always passes; without a file configuration nothing could have been
// published through the web, so there is nothing to compare with.
func (s *TemplateStore) RequireTrusted(data []byte, subject string) error {
	active, _, err := s.ReadCurrent()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: прочитать активный шаблон: %w", subject, err)
	}
	if string(active) == string(data) {
		return nil
	}
	trusted, err := ParseYAML(active)
	if err != nil {
		return fmt.Errorf("%s: активный шаблон: %w", subject, err)
	}
	submitted, err := ParseYAML(data)
	if err != nil {
		return fmt.Errorf("%s: %w", subject, err)
	}
	if problems := submitted.untrustedRunProblems(trusted); len(problems) > 0 {
		return &TrustedConfigError{Subject: subject, Problems: problems}
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
