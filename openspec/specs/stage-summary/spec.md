## Purpose

Спецификация определяет нормативное поведение capability `stage-summary`.

## Requirements
### Requirement: Stage summary файл
Система MUST читать `.stage-summary/{agent}.md` после каждого агента и включать его в HTML-отчёт.

#### Scenario: Summary в stage-отчёте
- **КОГДА** агент завершается
- **И** существует `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md`
- **ТОГДА** HTML-отчёт MUST отобразить содержимое этого файла как summary
- **И** summary MUST быть обрезан до 200 символов для краткого вида

#### Scenario: Summary отсутствует
- **КОГДА** файл `.stage-summary/{agent}.md` не существует
- **ТОГДА** HTML-отчёт MUST показать `—` вместо summary
- **И** MUST NOT выдавать ошибку

### Requirement: Summary в итоговом отчёте
Итоговый отчёт MUST показывать summary для каждого этапа.

#### Scenario: Таблица этапов с summary
- **КОГДА** пайплайн завершается
- **ТОГДА** итоговый HTML-отчёт MUST включать колонку со summary для каждого этапа
