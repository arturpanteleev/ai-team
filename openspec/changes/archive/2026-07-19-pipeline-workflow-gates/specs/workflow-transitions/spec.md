## MODIFIED Requirements

### Requirement: Поле transition в конфигурации агента
Каждый агент в `pipeline` конфига ДОЛЖЕН поддерживать поле `transition` со значениями `auto`, `by_confirm` или `gate`.

#### Scenario: По умолчанию auto
- **КОГДА** `transition` не указан в конфигурации агента
- **ТОГДА** система ДОЛЖНА использовать значение `auto`

#### Scenario: by_confirm останавливает пайплайн
- **КОГДА** агент с `transition: by_confirm` завершается успешно
- **ТОГДА** система ДОЛЖНА вывести в консоль: `Continue to <next-agent>? [Y/n/diff/summary]`
- **И** ДОЛЖНА ожидать ввод от пользователя

#### Scenario: gate останавливает пайплайн
- **КОГДА** агент с `transition: gate` завершается успешно
- **И** следующий агент имеет `gate_before: true`
- **ТОГДА** pipeline ДОЛЖЕН остановиться и показать резюме фазы
- **И** вывести: `Gate: перед {next-agent}. Продолжить? [Y/n]`
