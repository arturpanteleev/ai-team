## MODIFIED Requirements

### Requirement: Передача ролевой конфигурации в пайплайн
Система MUST применять индивидуальные настройки (model, effort, cli) при запуске каждого агента: model — аргументом CLI, effort — служебной секцией промпта.

#### Scenario: Запуск агента с индивидуальной моделью
- **КОГДА** конфиг coder-а указывает `model: <model-id>`
- **ТОГДА** пайплайн MUST запустить coder с флагом модели CLI (для opencode — `-m <model-id>`)
- **И** это MUST NOT влиять на модель других агентов

#### Scenario: Значение auto
- **КОГДА** model равен `auto` или пуст
- **ТОГДА** флаг модели НЕ передаётся (CLI использует свою модель по умолчанию)

### Requirement: Effort per agent
Система MUST поддерживать три уровня effort: `low`, `medium`, `high` и доводить значение до агента через промпт.

#### Scenario: Передача effort агенту
- **КОГДА** конфиг агента указывает `effort: high`
- **ТОГДА** служебная секция промпта MUST содержать уровень усилий high с краткой расшифровкой
- **И** если effort не указан — используется глобальное значение или `medium`

## ADDED Requirements

### Requirement: Поля timeout и on_negative_verdict в AgentConfig
Конфиг MUST поддерживать поля `timeout` (per-agent) и `on_negative_verdict` для каждого агента, а также глобальное поле `stage_timeout`.

#### Scenario: Парсинг новых полей
- **КОГДА** config.yaml содержит `stage_timeout: 30m` и у агента `timeout: 45m`, `on_negative_verdict: ask`
- **ТОГДА** система MUST распарсить значения и применить: таймаут агента 45m, остальным — 30m

### Requirement: Валидация конфигурации до запуска
Система MUST валидировать конфигурацию до запуска первого агента (fail fast).

#### Scenario: Неизвестный агент в pipeline
- **КОГДА** config.yaml содержит агента, отсутствующего в registry
- **ТОГДА** `ai-team run` MUST завершиться с ошибкой ДО выполнения какого-либо агента

#### Scenario: Невалидные значения полей
- **КОГДА** `transition`, `effort`, `on_negative_verdict` или `timeout` содержат недопустимые значения
- **ТОГДА** система MUST вернуть ошибку с перечислением допустимых значений
