## ADDED Requirements

### Requirement: Stage summary в HTML
HTML-шаблон stage.html ДОЛЖЕН отображать summary из `.stage-summary/{agent}.md`.

#### Scenario: Summary в stage-отчёте
- **КОГДА** открывается stage.html для агента
- **ТОГДА** страница ДОЛЖНА содержать секцию Summary с текстом из .stage-summary файла

### Requirement: Summary в итоговом отчёте
HTML-шаблон final.html ДОЛЖЕН включать summary для каждого этапа.

#### Scenario: Колонка summary в итоговой таблице
- **КОГДА** открывается final.html
- **ТОГДА** таблица этапов ДОЛЖНА содержать колонку Summary
