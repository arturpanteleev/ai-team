package main

import (
	"fmt"
	"io"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// warnAboutStoredTemplateCommands tells the operator at web start which stored
// template versions carry commands (gates, adapters, checks, MCP servers) or
// infrastructure settings that the active .ai-team/config.yaml does not contain.
// Before the trusted-config rule a template published through the web could
// hold them. The store has no provenance, so this is a warning to review, not a
// refusal to run.
func warnAboutStoredTemplateCommands(out io.Writer, target string) {
	store, err := config.NewTemplateStore(target)
	if err != nil {
		return
	}
	findings, err := store.AuditVersions()
	if err != nil || len(findings) == 0 {
		return
	}
	_, _ = fmt.Fprintf(out, "⚠ %d сохранённых версий шаблона (.ai-team/templates/versions) содержат команды или инфраструктурные настройки, которых нет в активном .ai-team/config.yaml.\n", len(findings))
	_, _ = fmt.Fprintln(out, "  До версии с правилом trusted_config_only такие версии мог опубликовать любой участник с правом template.edit; проверьте их и при необходимости удалите (задачи, закреплённые за версией, продолжат её использовать).")
	for _, finding := range findings {
		_, _ = fmt.Fprintf(out, "  - %s: %s\n", finding.Version, finding.Problems[0])
		if rest := len(finding.Problems) - 1; rest > 0 {
			_, _ = fmt.Fprintf(out, "    и ещё %d\n", rest)
		}
	}
}
