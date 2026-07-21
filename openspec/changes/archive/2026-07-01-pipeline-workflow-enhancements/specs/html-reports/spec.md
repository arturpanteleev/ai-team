## ADDED Requirements

### Requirement: Stage summary в HTML
HTML-шаблон stage.html MUST отображать summary из `.stage-summary/{agent}.md`.

#### Scenario: Summary в stage-отчёте
- **КОГДА** открывается stage.html для агента
- **ТОГДА** страница MUST содержать секцию Summary с текстом из .stage-summary файла

### Requirement: Summary в итоговом отчёте
HTML-шаблон final.html MUST включать summary для каждого этапа.

#### Scenario: Колонка summary в итоговой таблице
- **КОГДА** открывается final.html
- **ТОГДА** таблица этапов MUST содержать колонку Summary
