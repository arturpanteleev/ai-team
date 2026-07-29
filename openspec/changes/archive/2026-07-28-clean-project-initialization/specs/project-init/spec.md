## MODIFIED Requirements

### Requirement: Gitignore
В Git-репозитории система MUST исключать `.ai-team/` без изменения файлов
рабочего дерева по умолчанию. Пользователь MUST иметь явную возможность
записать разделяемое правило в `.gitignore`.

#### Scenario: Чистая инициализация по умолчанию
- **КОГДА** пользователь запускает `ai-team init` в Git-репозитории
- **ТОГДА** система MUST идемпотентно добавить `.ai-team/` в локальный
  `.git/info/exclude`
- **И** MUST NOT создавать или изменять `.gitignore`
- **И** `git status --porcelain` MUST остаться пустым, если workspace был
  чистым до запуска

#### Scenario: Явная разделяемая ignore policy
- **КОГДА** пользователь запускает `ai-team init --write-gitignore`
- **ТОГДА** система MUST идемпотентно добавить `.ai-team/` в корневой
  `.gitignore`
- **И** изменение `.gitignore` MUST быть видимым в Git workspace

#### Scenario: Linked worktree
- **КОГДА** `init` запускается в linked Git worktree
- **ТОГДА** система MUST получить фактический ignore path через Git
- **И** MUST NOT предполагать, что `.git` является каталогом

#### Scenario: Небезопасный ignore path
- **КОГДА** целевой ignore-файл является symlink
- **ТОГДА** `init` MUST завершиться ошибкой
- **И** MUST NOT записывать данные по symlink

### Requirement: Директория reports при инициализации
Система MUST создавать `.ai-team/reports/` при `ai-team init`.

#### Scenario: Init создаёт reports
- **КОГДА** `ai-team init` запускается
- **ТОГДА** система MUST создать `.ai-team/reports/` директорию
- **И** `.ai-team/` MUST быть исключён выбранной Git ignore policy
