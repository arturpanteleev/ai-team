## MODIFIED Requirements

### Requirement: Готовность delivery

Preflight MUST проверять GitHub CLI, authentication и remote только когда
скомпилированный workflow содержит delivery stage. Delivery stage — это и
агент вида `delivery`, и этап шаблона с блоком `delivery` (например, этап
реализации агентом, PR для которого создаёт контроллер до ревью). Worker,
делегирующий delivery контроллеру через controller API, эти проверки не
выполняет: их выполняет preflight контроллера.

#### Scenario: Workflow без delivery

- **КОГДА** pipeline не содержит delivery stage
- **ТОГДА** отсутствие `gh` MUST NOT блокировать run

#### Scenario: Delivery требует GitHub

- **КОГДА** pipeline содержит delivery stage и `gh auth status` неуспешен
- **ТОГДА** readiness MUST быть false до начала run

#### Scenario: Этап с блоком delivery требует GitHub и origin

- **КОГДА** этап шаблона объявляет блок `delivery`, а `gh` недоступен или remote `origin` не настроен
- **ТОГДА** readiness MUST быть false до начала run, даже без агента вида `delivery`

#### Scenario: Worker с доставкой через контроллер

- **КОГДА** disposable worker работает через controller API, и delivery (push и PR) выполняет контроллер
- **ТОГДА** preflight worker MUST NOT требовать `gh`, authentication и `origin`
- **И** preflight контроллера для того же шаблона MUST по-прежнему их требовать
