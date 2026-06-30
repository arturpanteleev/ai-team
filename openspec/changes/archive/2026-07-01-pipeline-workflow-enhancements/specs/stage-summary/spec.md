## ADDED Requirements

### Requirement: Stage summary файл
Система ДОЛЖНА читать `.stage-summary/{agent}.md` после каждого агента и включать его в HTML-отчёт.

#### Scenario: Summary в stage-отчёте
- **КОГДА** агент завершается
- **И** существует `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md`
- **ТОГДА** HTML-отчёт ДОЛЖЕН отобразить содержимое этого файла как summary
- **И** summary ДОЛЖЕН быть обрезан до 200 символов для краткого вида

#### Scenario: Summary отсутствует
- **КОГДА** файл `.stage-summary/{agent}.md` не существует
- **ТОГДА** HTML-отчёт ДОЛЖЕН показать `—` вместо summary
- **И** НЕ ДОЛЖЕН выдавать ошибку

### Requirement: Summary в итоговом отчёте
Итоговый отчёт ДОЛЖЕН показывать summary для каждого этапа.

#### Scenario: Таблица этапов с summary
- **КОГДА** пайплайн завершается
- **ТОГДА** итоговый HTML-отчёт ДОЛЖЕН включать колонку со summary для каждого этапа
