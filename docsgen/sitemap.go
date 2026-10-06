package docsgen

// Карта сайта документации ai-team. Её использует и команда docsgen, и
// сквозной тест сборки, поэтому новая страница добавляется только здесь.
//
// Разделы следуют схеме Django: знакомство, учебник (один сквозной пример),
// руководства (как решить задачу), справочник (точные факты) и устройство
// самого ai-team.
const (
	SectionStart     = "Знакомство"
	SectionTutorial  = "Учебник"
	SectionGuides    = "Руководства"
	SectionReference = "Справочник"
	SectionInside    = "Внутри ai-team"
)

// SiteConfig returns the ai-team documentation site configuration without
// the machine-specific fields (Root, Output, Version, BasePath).
func SiteConfig() Config {
	return Config{
		Title:        "ai-team",
		GitHubRepo:   "arturpanteleev/ai-team",
		SectionOrder: []string{SectionStart, SectionTutorial, SectionGuides, SectionReference, SectionInside},
		Aliases:      map[string]string{"README.md": "/"},
		HeaderLinks: []NavLink{
			{Title: "Как это выглядит", URL: "/start/tour/"},
			{Title: "Учебник", URL: "/tutorial/install/"},
			{Title: "Сравнение", URL: "/start/compare/"},
			{Title: "GitHub", URL: "https://github.com/arturpanteleev/ai-team"},
		},
		HeroActions: []NavLink{
			{Title: "Пройти учебник", URL: "/tutorial/install/"},
			{Title: "Посмотреть, как это работает", URL: "/start/tour/"},
		},
		Sources: []SourcedPage{
			{Source: "docs/index.md", Title: "ai-team", URL: "/"},

			{Source: "docs/start/tour.md", Section: SectionStart, Weight: 0, URL: "/start/tour/",
				Description: "Один прогон от задачи до pull request в скриншотах"},
			{Source: "docs/start/concepts.md", Section: SectionStart, Weight: 1, URL: "/start/concepts/",
				Description: "Прогон, кандидат, проверки, план поставки и глоссарий"},
			{Source: "docs/start/fit.md", Section: SectionStart, Weight: 2, URL: "/start/fit/",
				Description: "Кому ai-team поможет, а кому пока нет"},
			{Source: "docs/start/compare.md", Section: SectionStart, Weight: 3, URL: "/start/compare/",
				Description: "Devin, Copilot, OpenHands, LangGraph и ещё 9 решений"},

			{Source: "docs/tutorial/install.md", Section: SectionTutorial, Weight: 0, URL: "/tutorial/install/",
				Description: "Бинарник с проверкой подписи или сборка из исходников"},
			{Source: "docs/tutorial/first-run.md", Section: SectionTutorial, Weight: 1, URL: "/tutorial/first-run/",
				Description: "Задача для агентов-заглушек, без ключей и модели"},
			{Source: "docs/tutorial/approve.md", Section: SectionTutorial, Weight: 2, URL: "/tutorial/approve/",
				Description: "Прочитать план, подтвердить SHA-256 и получить PR"},
			{Source: "docs/tutorial/real-model.md", Section: SectionTutorial, Weight: 3, URL: "/tutorial/real-model/",
				Description: "OpenCode, Codex или Claude Code на вашем репозитории"},

			{Source: "docs/guides/ci-gate.md", Section: SectionGuides, Weight: 0, URL: "/guides/ci-gate/",
				Description: "Детерминированная проверка PR в GitHub Actions"},
			{Source: "docs/guides/project-checks.md", Section: SectionGuides, Weight: 1, URL: "/guides/project-checks/",
				Description: "Тесты и линтеры, которые контроллер запускает сам"},
			{Source: "docs/guides/troubleshooting.md", Section: SectionGuides, Weight: 2, URL: "/guides/troubleshooting/",
				Description: "Почему прогон остановился и как продолжить"},
			{Source: "docs/guides/evidence.md", Section: SectionGuides, Weight: 3, URL: "/guides/evidence/",
				Description: "verify, export и redact для доказательств прогона"},
			{Source: "docs/guides/dashboard.md", Section: SectionGuides, Weight: 4, URL: "/guides/dashboard/",
				Description: "Прогоны, решения и артефакты в браузере"},

			{Source: "docs/reference/cli.md", Section: SectionReference, Weight: 0, URL: "/reference/cli/",
				Description: "Все команды и флаги"},
			{Source: "docs/reference/config.md", Section: SectionReference, Weight: 1, URL: "/reference/config/",
				Description: "Поля .ai-team/config.yaml и профили init"},
			{Source: "docs/reference/pipeline.md", Section: SectionReference, Weight: 2, URL: "/reference/pipeline/",
				Description: "Кто из агентов что делает и какой вердикт выносит"},
			{Source: "docs/reference/statuses.md", Section: SectionReference, Weight: 3, URL: "/reference/statuses/",
				Description: "Что значит каждый статус и код выхода"},
			{Source: "docs/reference/security.md", Section: SectionReference, Weight: 4, URL: "/reference/security/",
				Description: "Что ai-team гарантирует и чего не обещает"},

			{Source: "docs/ARCHITECTURE.md", Title: "Архитектура", Section: SectionInside, Weight: 0, URL: "/architecture/"},
			{Source: "docs/MODULES.md", Title: "Карта модулей", Section: SectionInside, Weight: 1, URL: "/modules/"},
			{Source: "docs/DEVELOPMENT.md", Title: "Разработка ai-team", Section: SectionInside, Weight: 2, URL: "/development/"},
			{Source: "CONTRIBUTING.md", Title: "Как участвовать", Section: SectionInside, Weight: 3, URL: "/contributing/"},
			{Source: "SECURITY.md", Title: "Сообщить об уязвимости", Section: SectionInside, Weight: 4, URL: "/security/"},
			{Source: "CODE_OF_CONDUCT.md", Title: "Кодекс поведения", Section: SectionInside, Weight: 5, URL: "/code-of-conduct/"},
			{Source: "CHANGELOG.md", Title: "История изменений", Section: SectionInside, Weight: 6, URL: "/changelog/"},
		},
	}
}
