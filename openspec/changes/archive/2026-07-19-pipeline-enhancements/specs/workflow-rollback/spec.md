## ADDED Requirements

### Requirement: Флаг --retry-from
CLI команда `run` MUST поддерживать флаг `--retry-from <agent-name>`.

#### Scenario: Перезапуск с указанного агента
- **КОГДА** пользователь запускает `ai-team run --retry-from coder`
- **ТОГДА** пайплайн MUST пропустить всех агентов до `coder` (включительно: analyst, architect — пропущены)
- **И** начать выполнение с агента `coder`
- **И** артефакты пропущенных этапов MUST NOT удаляться

### Requirement: Проверка входных артефактов при retry
Система MUST проверять наличие всех необходимых входных артефактов перед запуском с указанного этапа.

#### Scenario: Ошибка при отсутствии артефактов
- **КОГДА** пользователь запускает `--retry-from deployer`, но артефакты tester отсутствуют
- **ТОГДА** система MUST вывести ошибку: `missing artifacts from previous stage: tester`
- **И** MUST NOT запускать агента

### Requirement: Повторный запуск завершённого этапа
`--retry-from` MUST позволять перезапускать уже завершённый агент.

#### Scenario: Retry на последнем агенте
- **КОГДА** deployer завершился с ошибкой
- **И** пользователь запускает `ai-team run --retry-from deployer`
- **ТОГДА** пайплайн MUST запустить только deployer заново
- **И** старые артефакты deployer MUST быть перезаписаны
