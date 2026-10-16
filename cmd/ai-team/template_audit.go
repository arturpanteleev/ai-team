package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// warnAboutStoredTemplateCommands tells the operator at web start which stored
// template versions carry commands (gates, adapters, checks, MCP servers) or
// infrastructure settings that the active .ai-team/config.yaml does not contain.
// Before the trusted-config rule a template published through the web could
// hold them. The store has no provenance, so the warning lists versions to
// review; running a run pinned to such a version is refused (trusted_config_only).
//
// It returns the reason the audit itself could not be completed (unreadable
// store or template); a missing project configuration is not an error, there is
// nothing to compare with.
func warnAboutStoredTemplateCommands(out io.Writer, target string) error {
	store, err := config.NewTemplateStore(target)
	if err != nil {
		return fmt.Errorf("хранилище шаблонов: %w", err)
	}
	findings, err := store.AuditVersions()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("аудит сохранённых версий шаблона: %w", err)
	}
	if len(findings) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(out, "⚠ %d сохранённых версий шаблона (.ai-team/templates/versions) содержат команды или инфраструктурные настройки, которых нет в активном .ai-team/config.yaml.\n", len(findings))
	_, _ = fmt.Fprintln(out, "  Запуск, resume и повторный запуск по таким версиям ОТКЛОНЯЮТСЯ (код trusted_config_only): задачи, закреплённые за ними, не продолжатся, пока команды не будут перенесены в .ai-team/config.yaml через review либо задача не будет начата заново по активному шаблону.")
	_, _ = fmt.Fprintln(out, "  До правила trusted_config_only такие версии мог опубликовать любой участник с правом template.edit; проверьте их и при необходимости удалите.")
	for _, finding := range findings {
		_, _ = fmt.Fprintf(out, "  - %s: %s\n", finding.Version, finding.Problems[0])
		if rest := len(finding.Problems) - 1; rest > 0 {
			_, _ = fmt.Fprintf(out, "    и ещё %d\n", rest)
		}
	}
	return nil
}
