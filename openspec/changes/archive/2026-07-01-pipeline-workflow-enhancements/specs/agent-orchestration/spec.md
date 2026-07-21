## ADDED Requirements

### Requirement: Git diff guard в пайплайне
Пайплайн MUST проверять git diff после агентов с пустыми outputs.

#### Scenario: Проверка после coder-а
- **КОГДА** coder завершается
- **ТОГДА** пайплайн MUST запустить `hasGitChanges(targetDir)`
- **И** если false — остановиться с ошибкой

### Requirement: by_confirm в пайплайне
Пайплайн MUST поддерживать паузу после этапов с `transition: by_confirm`.

#### Scenario: Пауза после этапа
- **КОГДА** агент с `by_confirm` завершается
- **ТОГДА** пайплайн MUST показать приглашение и ждать ввод
- **И** ЕСЛИ ввод `n` — завершить пайплайн досрочно

### Requirement: Loopback в пайплайне
Пайплайн MUST поддерживать возврат к coder-у при REJECTED/CHANGES_REQUESTED.

#### Scenario: Retry coder-а
- **КОГДА** reviewer вернул REJECTED
- **И** пользователь подтвердил retry
- **ТОГДА** пайплайн MUST перезапустить coder с review.md как входом
- **И** reviewer MUST запуститься снова после coder-а
