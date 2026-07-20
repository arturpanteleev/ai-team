## Purpose

Единые fail-closed checkpoint policies для интерактивных и автоматизированных запусков.

## Requirements

### Requirement: Единая checkpoint policy
Schema version 3 MUST поддерживать `checkpoint_before` и `checkpoint_after` со значениями `auto_continue`, `interactive`, `require_explicit`.

#### Scenario: Checkpoint после этапа
- **КОГДА** `checkpoint_after: require_explicit` назначен успешно завершённому этапу
- **ТОГДА** pipeline MUST показать статус, вердикт, длительность и артефакты
- **И** MUST получить интерактивное подтверждение либо `--approve-gates`

#### Scenario: Checkpoint перед этапом
- **КОГДА** `checkpoint_before: interactive` назначен следующему этапу
- **ТОГДА** pipeline MUST показать сводку актуальных попыток перед запросом

### Requirement: Неинтерактивный fail-closed режим
Checkpoint, требующий решения, MUST NOT автоматически подтверждаться при отсутствии terminal stdin.

#### Scenario: CI без разрешения
- **КОГДА** checkpoint достигнут в non-interactive процессе без `--approve-gates`
- **ТОГДА** run MUST завершиться со статусом stopped и exit code 3

### Requirement: Отдельное delivery approval
Обычный checkpoint approval MUST NOT разрешать commit, push или PR. Delivery
MUST требовать approval точного SHA-256 canonical plan.

#### Scenario: Gates подтверждены, delivery нет
- **КОГДА** передан `--approve-gates`, но не передан совпадающий `--approve-plan`
- **ТОГДА** delivery MUST остановиться после публикации плана и до внешних side effects
