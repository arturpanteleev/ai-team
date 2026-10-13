## MODIFIED Requirements

### Requirement: Готовность delivery

Preflight MUST проверять GitHub CLI, authentication и remote только когда
скомпилированный workflow содержит delivery stage. Delivery stage — это и
агент вида `delivery`, и этап шаблона с блоком `delivery` (например, этап
реализации агентом, PR для которого создаёт контроллер до ревью).

#### Scenario: Workflow без delivery

- **КОГДА** pipeline не содержит delivery stage
- **ТОГДА** отсутствие `gh` MUST NOT блокировать run

#### Scenario: Delivery требует GitHub

- **КОГДА** pipeline содержит delivery stage и `gh auth status` неуспешен
- **ТОГДА** readiness MUST быть false до начала run

#### Scenario: Этап с блоком delivery требует GitHub и origin

- **КОГДА** этап шаблона объявляет блок `delivery`, а `gh` недоступен или remote `origin` не настроен
- **ТОГДА** readiness MUST быть false до начала run, даже без агента вида `delivery`
