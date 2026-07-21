## MODIFIED Requirements

### Requirement: Stage summary файл
Каждый агент MUST записывать краткое резюме этапа в `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md` (требование доводится до агента служебной секцией промпта); система MUST читать этот файл после каждого агента и включать его в HTML-отчёт.

#### Scenario: Требование в промпте
- **КОГДА** система собирает промпт любого агента
- **ТОГДА** служебная секция MUST содержать путь `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md` и требование записать туда 2–5 строк резюме

#### Scenario: Summary в stage-отчёте
- **КОГДА** агент завершается
- **И** существует `.ai-team/artifacts/{feature}/.stage-summary/{agent}.md`
- **ТОГДА** HTML-отчёт MUST отобразить содержимое этого файла как summary
- **И** summary MUST быть обрезан до 200 символов (по рунам, не по байтам)

#### Scenario: Summary отсутствует
- **КОГДА** файл `.stage-summary/{agent}.md` не существует
- **ТОГДА** HTML-отчёт MUST показать `—` вместо summary
- **И** MUST NOT выдавать ошибку (summary — рекомендация, не гейт)
