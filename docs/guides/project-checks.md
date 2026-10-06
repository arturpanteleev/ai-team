# Настроить проверки проекта

Проверки (checks) — это команды вашего проекта, которые ai-team запускает сам,
без участия модели: тесты, линтер, сборка. По их результату контроллер решает,
можно ли доверять изменению. Здесь вы узнаете, где ai-team берёт список
проверок, как описать их для Go и Python и как подсмотреть их в CI проекта.

> [!LEARN]
> - где описываются проверки для `ai-team run` и для `ai-team gate`;
> - чем обычная команда отличается от типизированной проверки и почему для
>   поставки нужна именно типизированная;
> - как `ai-team init` сам находит проверки у Go-проекта;
> - как подключить pytest через JUnit-отчёт;
> - что умеет и чего не умеет `ai-team ci-import`.

## Что понадобится

- Собранный `ai-team` в `PATH` (см. [Установка](../tutorial/install.md)).
- Git-репозиторий проекта, в котором тесты уже запускаются одной командой.
- Для `ai-team run` — выполненный `ai-team init`.

## Где ai-team ищет проверки

| Команда | Файл | Где в файле |
|---|---|---|
| `ai-team run` | `.ai-team/config.yaml` | Поле `checks` у этапа конвейера, обычно у `tester` |
| `ai-team gate` | `gate.yaml` в корне проекта, затем `.ai-team/gate.yaml`, либо путь из `--config` | Поле `checks` верхнего уровня |

Формат одной проверки в обоих файлах одинаковый, поэтому описание можно
переносить из одного файла в другой. Проверки этапа запускаются после того,
как агент этого этапа закончил работу, в каталоге
[кандидата](../start/concepts.md#кандидат) (candidate worktree).

> [!IMPORTANT]
> Без проверки поставка невозможна. Перед commit, push и PR контроллер ищет
> успешную обязательную проверку класса `unit`, `integration` или `e2e` с
> адаптером `go-test-json` или `junit-xml`, в которой нашлись и прошли тесты.
> Иначе прогон остановится с сообщением `delivery запрещён: нет успешно
> выполненного required check класса unit/integration/e2e …`. Положительный
> вердикт модели проверку не заменяет.

## Поля проверки

| Поле | Обязательно | Значения и смысл |
|---|---|---|
| `name` | да | Уникальное имя внутри этапа (или `gate.yaml`) |
| `class` | да | `formatter`, `lint`, `build`, `unit`, `integration`, `e2e`, `coverage`, `race`, `security` |
| `command` | да | Список аргументов: `["go", "test", "./..."]`. Это не строка shell: `&&`, `|` и подстановки не работают без явного `["sh", "-c", "…"]` |
| `policy` | да | `required` — провал останавливает этап; `optional` — провал или отсутствие инструмента записываются, но не останавливают |
| `adapter` | нет | `command` (по умолчанию), `go-test-json`, `junit-xml` — как читать результат |
| `report_file` | для `junit-xml` | Путь к JUnit-отчёту относительно корня проекта |
| `timeout` | нет | Длительность в формате Go: `15m`, `1h` |
| `working_dir` | нет | Каталог запуска относительно корня проекта; выйти за пределы проекта нельзя |

Неизвестное поле или значение — ошибка загрузки конфигурации, например
`check ruff: неизвестный class "linter"`. Полная схема — в справочнике
[Конфигурация](../reference/config.md).

### Адаптеры

| Адаптер | Что учитывает | Ограничения |
|---|---|---|
| `command` | Только код выхода команды | Не годится как доказательство для поставки |
| `go-test-json` | Поток `go test -json`: сколько тестов найдено, прошло, упало | Команда должна начинаться с `go test` и содержать `-json`; только классы `unit`, `integration`, `e2e` |
| `junit-xml` | JUnit-отчёт из `report_file` | Только классы `unit`, `integration`, `e2e` |

Для `junit-xml` главный источник — отчёт. Если команда вернула 0, а в отчёте
есть упавшие тесты, проверка провалена. Если команда упала, проверка тоже
провалена, а числа из отчёта всё равно попадают в доказательства.

> [!PITFALL]
> Проверка обязана только читать проект. Контроллер сравнивает отпечаток рабочего
> дерева до и после команды; разрешено менять лишь `report_file`. Если команда
> создаёт кеш или артефакты сборки в проекте, проверка провалится с причиной
> `check изменил workspace; verification commands должны быть read-only`.
> Каталоги `.git`, `.ai-team`, `node_modules`, `vendor`, `dist`, `.venv` и
> `__pycache__` в сравнении не участвуют. Для `ai-team run` список можно
> дополнить в `.ai-team/config.yaml` через `tree_hash.ignore_dirs`; у `gate.yaml`
> такой настройки нет.

## Go: проверки находятся сами

Если в корне проекта есть `go.mod`, команда `ai-team init` добавит две
обязательные проверки к этапу `tester`:

```text
✓ Обнаружен verification profile: go
✓ .ai-team/ исключён через …/.git/info/exclude
✓ .ai-team/ инициализирован в …
```

В `.ai-team/config.yaml` появится:

```yaml
pipeline:
    # …
    - name: tester
      checks:
        - name: go-test
          class: unit
          adapter: go-test-json
          command: [go, test, -json, -count=1, ./...]
          policy: required
          timeout: 20m
        - name: go-vet
          class: lint
          command: [go, vet, ./...]
          policy: required
          timeout: 10m
```

Это обычный YAML: команды можно поменять, например добавить `-race` или
теги сборки. Для `gate.yaml` те же проверки пишутся в поле `checks` верхнего
уровня.

> [!NOTE]
> Если вы переименовали или убрали этап `tester`, `init` предупредит:
> `обнаружен go-профиль, но в pipeline нет стадии "tester" — required checks
> не присвоены`. Тогда добавьте проверки вручную к нужному этапу.

## Python: pytest через JUnit-отчёт

Для других стеков `init` проверки не угадывает и прямо об этом пишет:

```text
Предупреждение: тестовый профиль не обнаружен; delivery будет запрещён до настройки required unit/integration/e2e check
```

Опишите проверки сами. Для pytest используйте адаптер `junit-xml`: pytest
пишет отчёт, ai-team читает его.

1. Добавьте отчёт и кеши в `.gitignore`, чтобы они не делали рабочее дерево
   грязным:

   ```text
   report.xml
   __pycache__/
   .pytest_cache/
   ```

2. Добавьте проверки к этапу `tester` в `.ai-team/config.yaml`:

   ```yaml
   pipeline:
       # …
       - name: tester
         checks:
           - name: pytest
             class: unit
             adapter: junit-xml
             report_file: report.xml
             command: ["python", "-m", "pytest", "-p", "no:cacheprovider",
                       "--junitxml=report.xml", "-o", "junit_family=xunit2"]
             policy: required
             timeout: 15m
           - name: ruff
             class: lint
             command: ["ruff", "check", "."]
             policy: optional
   ```

3. Для CI положите те же проверки в `gate.yaml`:

   ```yaml
   schema_version: 1
   diff_policy:
     test_modify: required
   checks:
     - name: pytest
       class: unit
       adapter: junit-xml
       report_file: report.xml
       command: ["python", "-m", "pytest", "-p", "no:cacheprovider",
                 "--junitxml=report.xml", "-o", "junit_family=xunit2"]
       policy: required
   ```

4. Проверьте настройку без модели:

   ```bash
   ai-team gate --base HEAD~1 --candidate HEAD --out /tmp/gate-check
   ```

   В выводе должна появиться строка вида:

   ```text
     check pytest           ✓        passed (366ms)
   ```

> [!PITFALL]
> Флаг `-p no:cacheprovider` обязателен. Без него pytest создаёт каталог
> `.pytest_cache` в проекте, и проверка проваливается как изменившая рабочее
> дерево, даже если все тесты зелёные.

> [!TIP]
> Вывод gate показывает только `passed` или `failed`. Причину провала ищите в
> пакете: файл `checks/<номер>-<имя>.json`, поле `reason`. Там же лежат
> `stdout`, `stderr` и код выхода команды.

## Правило про тесты: `test_modify`

В `gate.yaml` есть ещё правило `diff_policy.test_modify`. Оно смотрит на
список изменённых файлов, а не на результаты тестов.

| Значение | Поведение |
|---|---|
| `required` | Изменён или добавлен код, но ни один тест не изменён и не добавлен — FAIL |
| `warning` | То же нарушение записывается как предупреждение, вердикт не меняется |
| `off` | Правило не проверяется |

Gate относит файл к тестам по пути:

- имя оканчивается на `_test.go` или содержит сегмент `.test.` или `.spec.`
  (`button.test.ts`, `api.spec.js`);
- файл лежит в каталоге `test`, `tests`, `__tests__`, `testdata`, `e2e`,
  `e2etest` или в каталоге с суффиксом `_test`.

Код — это файлы с известным расширением исходников (`.go`, `.py`, `.ts`,
`.java` и другие) вне тестовых путей. Манифесты зависимостей вроде
`pyproject.toml` и `go.mod`, файлы CI и сгенерированные каталоги правило не
затрагивают.

> [!PITFALL]
> Файл `test_app.py` в корне проекта gate считает кодом, а не тестом:
> префикс `test_` не распознаётся. Держите тесты Python в каталоге `tests/`,
> иначе изменение с тестом получит FAIL по `test_modify: required`.

## Подсмотреть проверки в CI: `ai-team ci-import`

Если тесты уже запускаются в GitHub Actions, `ci-import` покажет, какие шаги
ai-team может превратить в проверки. Команда ничего не запускает и ничего не
записывает: она читает `.github/workflows/*` и печатает предлагаемый набор.

```bash
ai-team ci-import
```

```text
✓ CI import: 3 checks (fingerprint 640c88633335)
• Effective suite (до запуска):
•   go-vet           lint       go vet ./...
•   golangci-lint    lint       golangci-lint run
•   go-test          unit       go test -race ./... -json
•   skip …/.github/workflows/ci.yml job=test step=1: шаг не входит в ограниченное объяснимое сопоставление
…
```

Сопоставление намеренно узкое:

- шаги `run:` с одной командой `go vet …`, `go build …` или `go test …`
  (к `go test` добавляется `-json`);
- действия `golangci/golangci-lint-action` и `github/codeql-action`;
- всё остальное, включая цепочки через `&&`, `|`, `;` и многострочные
  `run:`, пропускается с причиной `skip`.

Все импортированные проверки получают `policy: optional`. Перенесите нужные
строки в `.ai-team/config.yaml` вручную и решите, какие из них сделать
`required`. Для Python, Node и других стеков `ci-import` сейчас ничего не
находит. Команда работает только в проекте после `ai-team init`, иначе
завершается с кодом 1: `проект не инициализирован … — сначала выполните
ai-team init`.

## Что дальше

- [Проверки в CI без модели](ci-gate.md) — запустить те же проверки на каждом
  pull request.
- [Остановки и BLOCKED](troubleshooting.md) — что делать, если проверка
  остановила прогон.
- [Конфигурация](../reference/config.md) — полная схема `.ai-team/config.yaml`.
