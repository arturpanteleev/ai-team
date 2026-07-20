## ДОБАВЛЕННЫЕ Требования

### Requirement: Конфиг по умолчанию
Система MUST создавать `.ai-team/config.yaml` с разумными значениями по умолчанию.

#### Scenario: Структура конфига
- **КОГДА** `ai-team init` запускается
- **ТОГДА** config.yaml MUST содержать:
  - `pipeline: standard`
  - `cli: opencode`
  - `model: auto`

### Requirement: Кастомный путь конфига
Система MUST поддерживать флаг `--target` для указания директории целевого проекта.

#### Scenario: Init в кастомной директории
- **КОГДА** пользователь запускает `ai-team init --target /path/to/project`
- **ТОГДА** система MUST создать `/path/to/project/.ai-team/` вместо `./.ai-team/`

### Requirement: Gitignore
Система MUST добавлять `.ai-team/` в `.gitignore`, если его там нет.

#### Scenario: Авто-добавление в gitignore
- **КОГДА** `ai-team init` запускается и `.gitignore` существует
- **ТОГДА** система MUST дописать `.ai-team/` в `.gitignore`, если его там ещё нет
