## Purpose

Спецификация определяет нормативное поведение capability `git-diff-guard` как общего per-attempt mutation guard.

## Requirements

### Requirement: Атрибутированный mutation guard
Система MUST сравнить baseline и итоговое состояние после любого этапа, definition которого явно объявляет mutation policy.

#### Scenario: Coder не создал изменений
- **КОГДА** coder завершается с кодом 0
- **И** baseline текущей попытки не отличается от итогового состояния
- **ТОГДА** пайплайн MUST остановиться с ошибкой `coder не создал изменений в проекте`
- **И** последующие агенты MUST NOT выполняться

#### Scenario: Разрешённые изменения
- **КОГДА** stage изменил или создал файлы только в `allowed_paths`
- **ТОГДА** mutations MUST содержать нормализованные workspace-relative пути
- **И** пайплайн MUST продолжить выполнение

#### Scenario: Мутация вне scope
- **КОГДА** попытка изменила файл вне `allowed_paths`
- **ТОГДА** попытка MUST завершиться ошибкой независимо от exit code runtime

### Requirement: Проект без Git не ослабляет контроль
Если targetDir не является Git-репозиторием, система MUST использовать полный confined workspace snapshot, исключая только controller-owned `.git` и `.ai-team`.

#### Scenario: Проект без git
- **КОГДА** stage требует diff или является read-only, а targetDir не является Git-репозиторием
- **ТОГДА** система MUST сравнить хэши файлов и symlink targets до и после stage
- **И** MUST NOT пропускать guard

### Requirement: Существующая грязь не считается изменением stage
Baseline MUST фиксироваться непосредственно перед stage, а mutations MUST содержать только delta этой попытки.

#### Scenario: Pre-existing dirty workspace
- **КОГДА** до stage уже существует изменённый файл
- **И** stage его не изменяет
- **ТОГДА** файл MUST NOT считаться mutation текущей попытки и MUST NOT попасть в delivery plan

### Requirement: Read-only этапы контролируются
Этап с `mutation: read_only` MUST завершиться ошибкой при любой workspace mutation.

#### Scenario: Reviewer изменил исходник
- **КОГДА** read-only reviewer меняет файл проекта
- **ТОГДА** контроллер MUST отклонить попытку и остановить downstream execution

### Requirement: Набор исключений guard'а неконфигурируем
Per-attempt mutation guard MUST исключать из атрибуции ТОЛЬКО controller-owned
каталоги (`.git`, `.ai-team`). Ни baseline производительности обхода, ни
project-specific `tree_hash.ignore_dirs` MUST NOT сужать атрибуцию: иначе
`mutation: none` перестаёт означать read-only.

#### Scenario: Запись в каталог зависимостей или сборки
- **КОГДА** этап с `mutation: none` записал файл в `node_modules`, `vendor`,
  `dist`, `.venv` или `__pycache__`
- **ТОГДА** guard MUST отклонить попытку
- **И** путь MUST попасть в mutations манифеста попытки

#### Scenario: Сломан вход уже пройденной обязательной проверки
- **КОГДА** обязательная проверка прошла, читая файл из такого каталога
- **И** следующий этап с `mutation: none` изменил этот файл
- **ТОГДА** попытка MUST быть отклонена
- **И** delivery plan MUST NOT быть предъявлен человеку

### Requirement: Controller-owned метаданные под guard'ом
Guard MUST сравнивать `.ai-team` до и после этапа за вычетом подкаталогов,
которые контроллер ведёт сам, и MUST отклонять запись агента в `.ai-team` вне
declared artifact namespace. Каталог исключён из workspace snapshot только
потому, что туда пишет контроллер, — не потому, что он разрешён агенту.

#### Scenario: Агент положил файл в `.ai-team`
- **КОГДА** этап создал файл в `.ai-team` вне artifact namespace этапа
- **ТОГДА** попытка MUST завершиться ошибкой
- **И** путь MUST быть атрибутирован как mutation
