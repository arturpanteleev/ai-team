# Design — Configurable tree-hash ignore list (OPS-2)

## Контекст

`pkg/checks.WorkspaceDigest(target)` — единственная каноническая функция
tree hashing. Она вызывается из нескольких независимых путей: verification
(`pkg/checks`), кандидатное evidence (`pkg/candidate`), планирование и
исполнение delivery (`pkg/delivery/planner.go`/`executor.go`), pipeline
(`pkg/pipeline`). Критический инвариант целостности: всё это — **одно и то же
значение digest для одного workspace**. Если бы ignore-list различался между
этими точками, binding проверенного digest к delivery молча ломался бы.

Поэтому конфигурируемые ignore нельзя прокидывать только через часть
call-site'ов — нужен единый источник истины.

## Решение: процесс-глобальный extra-набор в pkg/checks

Один процесс управляет одним проектом (CLI/daemon держит target под workspace
lock). Следовательно, регистрация project-specific ignore-имён как
**процесс-глобального** дополнительного набора безопасна и гарантирует, что
все вычисления digest используют идентичный ignore-list без изменения ни одного
call-site сигнатурой.

- `pkg/checks/ignore.go`:
  - `SetExtraIgnoreDirs([]string)` — mutex-защищённая установка (копия во
    внутреннюю map), вызывается однократно при `config.Load` с валидированным
    `TreeHash.IgnoreDirs`.
  - `ResetExtraIgnoreDirs()` — для тестов и повторного конфига.
  - `DefaultIgnoreDirs()` — возвращает baseline (неизменяемый) + extra.
  - `ValidIgnoreDirName(string) bool` — строгий канонический валидатор.
- `pkg/config`: `Config.TreeHash *TreeHashConfig{IgnoreDirs []string}` (yaml
  `tree_hash`), строгая валидация имён + анти-дубликат, включена в
  `Config.Validate`. `Load()` применяет набор (`checks.SetExtraIgnoreDirs`).

## Строгость и безопасность

- Только простые имена каталогов: `[A-Za-z0-9._-]`, непустое, не `.`/`..`,
  без ведущего `-`, без `/`/`\`/NUL/пробелов, ≤128 байт.
- Поиск — по имени компонента на любой глубине walk'а (как baseline).
- Baseline (`DefaultIgnoreDirs`) не расширяется удалением — extra только
  добавляет имена. Ослабления digest identity невозможны.

## Почему не variadic/конtext-вариант

Вариант `WorkspaceDigestEx(target, extra)` потребовал бы threading через ~7
call-site'ов в трёх пакетах и создал бы риск mismatch (пропущенный call-site).
Глобальный набор устраняет класс ошибок "забрали ignore у одного пути",
сохраняя единственный источник истины.
