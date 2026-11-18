package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// cmdTemplate — разовая работа с происхождением шаблонов. Сейчас единственная
// подкоманда — audit: она показывает сохранённые версии, чьи команды и
// инфраструктура не совпадают с доверенным файловым конфигом.
func cmdTemplate() {
	if len(os.Args) < 3 {
		fatal("template требует подкоманду: audit")
	}
	switch os.Args[2] {
	case "audit":
		cmdTemplateAudit()
	default:
		fatal("неизвестная подкоманда template: %s", os.Args[2])
	}
}

// cmdTemplateAudit — dry-run по умолчанию: печатает список версий с
// недоверенными командами или инфраструктурой с идентификатором (SHA-256
// версии) и причиной. Явный --rewrite
// пересохраняет только выбранные версии через доверенный файловый якорь, не
// изменяя .ai-team/config.yaml. Без --rewrite команда ничего не записывает.
func cmdTemplateAudit() {
	flags := flag.NewFlagSet("template audit", flag.ExitOnError)
	targetValue := flags.String("target", "", "Путь к целевому проекту (по умолчанию текущая директория)")
	rewrite := flags.Bool("rewrite", false, "Пересохранить проверенные версии через доверенный файловый якорь")
	// FlagSet создан с flag.ExitOnError: Parse сам завершает процесс и ошибку не возвращает.
	_ = flags.Parse(os.Args[3:])
	target, err := absoluteTarget(*targetValue)
	if err != nil {
		fatal("template audit: %v", err)
	}
	store, err := config.NewTemplateStore(target)
	if err != nil {
		fatal("template audit: %v", err)
	}
	findings, err := store.AuditVersions()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fatal("template audit: %v", err)
	}
	if *rewrite {
		rewriteTemplateVersions(store, findings, flags.Args())
		return
	}
	printTemplateAudit(findings)
}

func printTemplateAudit(findings []config.TemplateVersionAudit) {
	if len(findings) == 0 {
		fmt.Println("Версий с недоверенными командами или инфраструктурой не найдено. Ничего не изменено.")
		fmt.Println("Чистые версии без якоря доверия запускаются по общему правилу: аудит отмечает только команды и инфраструктуру сверх доверенного файлового конфига.")
		return
	}
	fmt.Printf("Найдено %d версий шаблона с проблемами доверия (dry-run, ничего не изменено):\n", len(findings))
	for _, finding := range findings {
		fmt.Printf("  - %s: %s\n", finding.Version, strings.Join(finding.Problems, "; "))
	}
	fmt.Println("Запуск по сохранённым версиям с чужими командами отклоняется кодом trusted_config_only, пока команды и инфраструктура не будут перенесены в .ai-team/config.yaml через review.")
	fmt.Println("После проверки пересохраните нужную версию: ai-team template audit --rewrite <version>")
}

// rewriteTemplateVersions при явном --rewrite пересохраняет указанные версии,
// а если версии не заданы — все найденные аудитом. Каждая версия перезаписывается
// через доверенный файловый якорь; файловый конфиг не меняется.
func rewriteTemplateVersions(store *config.TemplateStore, findings []config.TemplateVersionAudit, ids []string) {
	if len(ids) == 0 {
		for _, finding := range findings {
			ids = append(ids, finding.Version)
		}
	}
	if len(ids) == 0 {
		fmt.Println("Пересохранять нечего: версий с недоверенными командами или инфраструктурой не найдено.")
		return
	}
	for _, id := range ids {
		if err := store.RewriteVersion(id); err != nil {
			fatal("template audit --rewrite %s: %v", id, err)
		}
		fmt.Printf("✓ версия %s пересохранена через доверенный файловый якорь (.ai-team/templates/trusted-versions/%s); .ai-team/config.yaml не изменён\n", id, id)
	}
	fmt.Println("Пересохранённые версии запускаются; все остальные по-прежнему проверяются trusted_config_only.")
}
