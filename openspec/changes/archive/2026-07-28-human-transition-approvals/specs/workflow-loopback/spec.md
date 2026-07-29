## MODIFIED Requirements

### Requirement: Loopback при REJECTED
Возврат после `REJECTED` или `CHANGES_REQUESTED` MUST выполняться только после
persisted human decision и MUST NOT зависеть от наличия TTY.

#### Scenario: CHANGES_REQUESTED без TTY
- **КОГДА** reviewer возвращает `CHANGES_REQUESTED` и retry доступен
- **ТОГДА** pipeline MUST создать approval с actions `return_to_coder`,
  `reject`, `request_information`, `override_approve`
- **И** decision `return_to_coder` MUST инвалидировать downstream attempts и
  продолжить тот же run с coder

#### Scenario: Решение reject
- **КОГДА** уполномоченный человек выбирает `reject`
- **ТОГДА** run MUST завершиться как rejected без запуска coder
