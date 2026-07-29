## ADDED Requirements

### Requirement: OpenCode preflight

До принятия нового run система MUST проверить configured OpenCode executable
и получить его версию ограниченной по времени командой.

#### Scenario: Version command зависла

- **КОГДА** OpenCode version command не завершается в установленный timeout
- **ТОГДА** preflight MUST завершить её
- **И** MUST вернуть failed check без запуска агента

#### Scenario: Безопасная диагностика окружения

- **КОГДА** preflight описывает credential allow-list
- **ТОГДА** сообщение MUST содержать только имена переменных и факт наличия
- **И** MUST NOT содержать значения credentials
