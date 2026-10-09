## Purpose

Спецификация определяет нормативное поведение capability `project-init`.
## Requirements
### Requirement: Конфиг по умолчанию
Система MUST создавать `.ai-team/config.yaml` с разумными значениями по умолчанию.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать:
  - `schema_version: 5`
  - one `template` ID and `title`
  - ordered `stages` with unique `id` values and human-readable `title`
  - `returns` that reference existing stages and point only backward
  - `cli: opencode`
  - `effort: medium`
  - generated template `idea-to-prod` when no profile override is requested

#### Scenario: Legacy schema v4
- **КОГДА** config loader встречает `schema_version: 4`
- **ТОГДА** config MUST быть отклонён с подсказкой запустить `ai-team init --force` или перенести его вручную по документации

#### Scenario: Явная перезапись конфига
- **КОГДА** пользователь запускает `ai-team init --force` в уже инициализированном проекте
- **ТОГДА** init MUST заменить существующий regular `.ai-team/config.yaml` шаблоном выбранного профиля
- **И** без `--force` пользовательский конфиг MUST сохраниться

### Requirement: Кастомный путь конфига
Система MUST поддерживать флаг `--target` для указания директории целевого проекта.

#### Scenario: Init в кастомной директории
- **КОГДА** пользователь запускает `ai-team init --target /path/to/project`
- **ТОГДА** система MUST создать `/path/to/project/.ai-team/` вместо `./.ai-team/`

### Requirement: Gitignore
В Git-репозитории система MUST исключать `.ai-team/` без изменения файлов
рабочего дерева по умолчанию. Пользователь MUST иметь явную возможность
записать разделяемое правило в `.gitignore`.

#### Scenario: Чистая инициализация по умолчанию
- **КОГДА** пользователь запускает `ai-team init` в Git-репозитории
- **ТОГДА** система MUST идемпотентно добавить `.ai-team/` в локальный
  `.git/info/exclude`
- **И** MUST NOT создавать или изменять `.gitignore`
- **И** `git status --porcelain` MUST остаться пустым, если workspace был
  чистым до запуска

#### Scenario: Явная разделяемая ignore policy
- **КОГДА** пользователь запускает `ai-team init --write-gitignore`
- **ТОГДА** система MUST идемпотентно добавить `.ai-team/` в корневой
  `.gitignore`
- **И** изменение `.gitignore` MUST быть видимым в Git workspace

#### Scenario: Linked worktree
- **КОГДА** `init` запускается в linked Git worktree
- **ТОГДА** система MUST получить фактический ignore path через Git
- **И** MUST NOT предполагать, что `.git` является каталогом

#### Scenario: Небезопасный ignore path
- **КОГДА** целевой ignore-файл является symlink
- **ТОГДА** `init` MUST завершиться ошибкой
- **И** MUST NOT записывать данные по symlink

### Requirement: Директория reports при инициализации
Система MUST создавать `.ai-team/reports/` при `ai-team init`.

#### Scenario: Init создаёт reports
- **КОГДА** `ai-team init` запускается
- **ТОГДА** система MUST создать `.ai-team/reports/` директорию
- **И** `.ai-team/` MUST быть исключён выбранной Git ignore policy

### Requirement: Обновлённый конфиг по умолчанию
Конфиг по умолчанию MUST включать schema v5, один процессный шаблон, `effort`, stage timeout и стек-специфичные deterministic checks.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать:
  - `template: idea-to-prod` и список `stages`
  - `cli: opencode`
  - `effort: medium`
  - `stage_timeout: 30m`

#### Scenario: Go stack
- **КОГДА** init обнаруживает Go project
- **ТОГДА** он MUST добавить required `go-test-json` check без shell-интерполяции
- **И** test command MUST отключать test cache через `-count=1`

#### Scenario: Stack без typed parser
- **КОГДА** init обнаруживает Rust, Python или Node project без поддержанного typed adapter
- **ТОГДА** он MUST NOT добавлять untyped command как доказательство тестов
- **И** MUST вывести warning

#### Scenario: Неизвестный стек
- **КОГДА** init не может определить verification profile
- **ТОГДА** он MUST вывести warning
- **И** delivery MUST оставаться запрещённым до настройки required unit/integration/e2e check

#### Scenario: Профили materialized as templates
- **КОГДА** пользователь запускает `init --profile fast`, `standard` или `regulated`
- **ТОГДА** config MUST содержать ровно один шаблон, выбранный из обычных `stages`, `returns` и `checks`
- **И** профиль MUST иметь собственный template ID, а не runtime-переключатель
