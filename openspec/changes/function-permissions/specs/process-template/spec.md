## ADDED Requirements

### Requirement: Функция подтверждающего этапа

Схема шаблона MUST допускать у этапа необязательное поле `approver_function` — функцию подтверждающего.
Значение MUST сопоставляться известной роли и MUST отличаться от `function`
этапа с учётом синонимов (`po`/`product_owner`, `deployer`/`release_manager`).
Если `approver_function` задан, сгенерированные approval подтверждения и
возврата этапа MUST требовать роль подтверждающего. Верхнеуровневое поле
`forbid_self_approval` MAY включать запрет подтверждения собственной работы;
по умолчанию оно выключено.

#### Scenario: Совпадающие функции

- **КОГДА** `approver_function` совпадает с `function` этапа или является её
  синонимом
- **ТОГДА** шаблон MUST быть отклонён до запуска задачи

#### Scenario: Подтверждающий в графе

- **КОГДА** у этапа `function: developer` и `approver_function: reviewer`
- **ТОГДА** approval перехода этапа вперёд и возврата MUST требовать роль
  `reviewer`
