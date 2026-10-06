# 2. Первая задача без модели

Здесь вы подготовите маленький Go-проект greeter и дадите `ai-team` задачу «добавить приветствие по имени». Прогон пройдёт все этапы и остановится перед delivery — ждать вашего решения. Ключи и модель не нужны: вместо модели работают агенты-заглушки.

> [!LEARN]
> - как подготовить проект и заглушки для учебного режима
> - что делает `ai-team init` и что появляется в `.ai-team/`
> - как читать вывод `ai-team run` по этапам
> - почему прогон останавливается с кодом 3 и что это значит

## Учебный режим с агентами-заглушками

В этом и следующем шаге вместо настоящей модели работает скрипт [e2etest/mock-opencode.sh](../../e2etest/mock-opencode.sh). Он притворяется CLI `opencode`: каждый «агент» мгновенно пишет заготовленный файл и ставит положительный вердикт. Вместо настоящего `gh` будет заглушка, которая ничего не отправляет на GitHub.

> [!IMPORTANT]
> Учебный режим показывает механику контроллера, а не качество модели. Заглушка-кодер не напишет функцию приветствия: она создаст файл `e2e_implementation.go` с константой, а заглушка-тестировщик — тест к нему. Зато всё остальное настоящее: отдельная копия репозитория, запуск `go test` и `go vet`, delivery-план, подтверждение, commit и push в Git.

Вам понадобится установленный `ai-team` и клон репозитория `ai-team` — из него берётся заглушка. Если ещё не сделали, пройдите [1. Установка](install.md).

## 1. Подготовить заглушки

Откройте терминал в корне клона `ai-team` и задайте две переменные: где лежит клон и где будет учебный каталог.

```bash
export AI_TEAM_SRC="$PWD"                 # корень клона ai-team
export TUT="$HOME/ai-team-tutorial"       # учебный каталог
mkdir -p "$TUT/mockbin"
```

Подставьте заглушку агентов под именем `opencode`:

```bash
ln -s "$AI_TEAM_SRC/e2etest/mock-opencode.sh" "$TUT/mockbin/opencode"
```

Создайте заглушку `gh`. Она отвечает «авторизован», на создание PR печатает выдуманный адрес и запоминает, что PR «создан»:

```bash
cat > "$TUT/mockbin/gh" <<'EOF'
#!/bin/sh
# Учебная заглушка gh: ничего не отправляет на GitHub.
marker="$(dirname "$0")/gh-pr-created"
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  echo "logged in (учебная заглушка)"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  [ -f "$marker" ] || exit 1
  printf '{"url":"https://example.test/pr/1","state":"OPEN","baseRefName":"main","headRefName":"%s","headRefOid":"%s"}\n' "$3" "$(git rev-parse HEAD)"
  exit 0
fi
touch "$marker"
echo "https://example.test/pr/1"
EOF
chmod +x "$TUT/mockbin/gh"
```

Поставьте каталог с заглушками первым в `PATH` и проверьте, что подхватились именно они:

```bash
export PATH="$TUT/mockbin:$PATH"
opencode --version
gh auth status
```

```text
opencode mock 1.0.0
logged in (учебная заглушка)
```

> [!WARNING]
> Пока в этом окне `PATH` начинается с `$TUT/mockbin`, команда `gh` здесь — заглушка. Для настоящей работы с GitHub откройте новое окно терминала. На шаге 4 заглушки уберём совсем.

## 2. Создать проект greeter

greeter — программа из одного файла, которая печатает «Hello, world!». Код кладём в `cmd/greeter/`, а корень модуля оставляем пустым: туда заглушка-кодер запишет свой файл.

```bash
mkdir -p "$TUT/greeter/cmd/greeter"
cd "$TUT/greeter"

cat > go.mod <<'EOF'
module example.com/greeter

go 1.26
EOF

cat > cmd/greeter/main.go <<'EOF'
package main

import "fmt"

func main() {
	fmt.Println("Hello, world!")
}
EOF

go run ./cmd/greeter
```

```text
Hello, world!
```

> [!PITFALL]
> Не кладите `main.go` в корень проекта. Заглушка-кодер пишет в корень файл с `package e2eimplementation`. Рядом с `package main` получится два пакета в одном каталоге, и `go test` с `go vet` упадут — прогон остановится на проверках.

## 3. Сделать из проекта Git-репозиторий

Для delivery `ai-team` нужен Git-репозиторий с веткой и удалённым репозиторием `origin`: туда он отправит ветку с результатом. Вместо GitHub возьмём локальный «голый» репозиторий.

```bash
git init -b main
git config user.name "Tutorial"
git config user.email "tutorial@example.test"
git add .
git commit -m "greeter: первая версия"

git init --bare -b main "$TUT/origin.git"
git remote add origin "$TUT/origin.git"
git push -u origin main
```

Начинайте прогон с чистого рабочего каталога: `git status --short` ничего не печатает.

## 4. Подключить ai-team к проекту

```bash
ai-team init
```

```text
✓ Обнаружен verification profile: go
✓ .ai-team/ исключён через ~/ai-team-tutorial/greeter/.git/info/exclude
✓ .ai-team/ инициализирован в ~/ai-team-tutorial/greeter
```

`init` создал каталог `.ai-team/`:

| Что | Зачем |
|---|---|
| `.ai-team/config.yaml` | этапы, переходы между ними, проверки, выбор CLI |
| `.ai-team/artifacts/` | документы агентов: спецификация, дизайн, ревью, отчёты |
| `.ai-team/logs/`, `.ai-team/reports/` | логи и HTML-отчёты по прогонам |

Каталог исключён через `.git/info/exclude`, поэтому `git status` по-прежнему чистый. Если правило нужно хранить в репозитории, есть `ai-team init --write-gitignore`.

Раз в проекте есть `go.mod`, `init` сам включил две обязательные проверки (checks): `go test -json -count=1 ./...` и `go vet ./...`. Загляните в `.ai-team/config.yaml` — они записаны у этапа `tester`. Подробно поля разобраны в [Конфигурации](../reference/config.md#проверки-checks).

> [!NOTE]
> Для проектов не на Go `init` предупредит, что delivery запрещена, пока вы не настроите обязательную проверку вручную. Как это сделать — [Настроить проверки проекта](../guides/project-checks.md).

## 5. Запустить задачу

```bash
ai-team run --feature greet-by-name --task "Добавить приветствие по имени в CLI" < /dev/null
```

`--feature` — короткое имя задачи (буквы, цифры, `-`, `_`, `.`), из него получится ветка `ai-team/greet-by-name`. `--task` — описание для агентов.

`< /dev/null` отключает ввод с клавиатуры. Без терминала `ai-team` ведёт себя как в CI: печатает delivery-план и останавливается, а подтверждение вы даёте отдельной командой. Так удобнее пройти учебник по шагам. Как выглядит интерактивный вариант — в конце страницы.

Прогон займёт несколько секунд. Вывод длинный, разберём его по частям.

![Вывод первого прогона: этапы, delivery-план и остановка](../assets/screens/terminal-run.png "Первый прогон останавливается перед delivery")

## 6. Разобрать вывод

### Предварительная проверка

Сначала контроллер проверяет окружение (preflight):

```text
  ✓ cli (required): opencode mock 1.0.0
  ! model: model/provider выбирает opencode
  ! credentials: явные credential environment variables не разрешены
  ✓ git_repository (required): ~/ai-team-tutorial/greeter
  ✓ git_branch (required): main
  ✓ delivery_remote (required): ~/ai-team-tutorial/origin.git
  ✓ github_auth (required): gh authentication доступна
```

`✓` — проверка пройдена, `!` — предупреждение, которое не мешает. Пункты с `(required)` нужны, чтобы прогон дошёл до конца, включая delivery. Если такой пункт помечен `✗`, исправьте окружение, прежде чем ждать результата. Два предупреждения про модель и ключи для учебного режима нормальны, к ним вернёмся на [шаге 4](real-model.md).

### Этапы

Дальше по очереди идут этапы. У каждого: `→` — что агент получил на вход, `✓` — что он создал, строки `MOCK:` печатает заглушка. Все файлы лежат в `.ai-team/worktrees/<run_id>/` — это отдельная копия репозитория, кандидат (candidate worktree, см. [Ключевые понятия](../start/concepts.md#кандидат)). Ваша ветка `main` всё это время не меняется.

```text
▶ coder
  → design ~/ai-team-tutorial/greeter/.ai-team/worktrees/…/design.md(…, 51 байт)
  → tasks ~/ai-team-tutorial/greeter/.ai-team/worktrees/…/tasks.md(…, 43 байт)
MOCK: agent=coder feature=greet-by-name mode=normal
MOCK:   created e2e_implementation.go
…
ai-team [coder] ✓ (100ms)
```

Кто есть кто:

| Этап | Что делает |
|---|---|
| `analyst` | превращает задачу в предложение и продуктовую спецификацию |
| `architect` | пишет технический дизайн и список работ |
| `coder` | пишет код по дизайну |
| `reviewer` | сверяет код со спецификацией и ставит вердикт `APPROVED` или `CHANGES_REQUESTED` |
| `tester` | пишет тесты; после него контроллер сам запускает обязательные проверки |
| `verifier` | итоговая сверка с критериями приёмки, вердикт `APPROVED` или возврат на доработку |
| `deployer` | не модель: контроллер собирает delivery-план, а после подтверждения делает commit, push и PR |

Вердикт агента — это мнение модели. Решение перейти дальше принимает контроллер: он сверяет, какие файлы агент тронул, и сам запускает проверки. У тестировщика в нашем прогоне ушло около двух секунд — это работали `go test` и `go vet`. Подробнее — [Агенты и этапы](../reference/pipeline.md).

### Delivery-план

Когда все этапы пройдены, контроллер печатает delivery-план (canonical delivery plan, см. [Ключевые понятия](../start/concepts.md#delivery-план)) — точный список того, что попадёт в commit:

```text
Canonical delivery plan:
{
  "schema_version": 3,
  "branch": "ai-team/greet-by-name",
  "base_branch": "main",
  "remote": "origin",
  "files": [
    "e2e_implementation.go",
    "e2e_implementation_test.go"
  ],
  …
  "commit_message": "greet-by-name Добавить приветствие по имени в CLI",
  …
}
Plan SHA-256: a80328fbb3c0b4b7685fecd564ac56d84de454944fdb5e8f2ae71de02bf487d3
```

У вас хеш будет другим: в план входят идентификатор прогона и хеш вашего начального коммита. Подробно план разберём на следующем шаге.

### Остановка

```text
Для продолжения с явным подтверждением плана: ai-team run --resume 20261006T152959.266311000Z-a661d54efaecd972 --approve-plan a80328fb…02bf487d3
ai-team [deployer] ⏸ delivery перед deployer: требуется решение человека (run=… approval=… subject=a80328fb…)

✗ Пайплайн остановлен: delivery перед deployer: требуется решение человека (…)
```

Красный крестик тут не ошибка. Прогон дошёл до delivery и остановился: commit, push и PR без человека не делаются. Проверьте код выхода:

```bash
echo $?
```

```text
3
```

Код `3` значит «прогон остановлен и ждёт решения человека». Код `0` — прогон завершён, `1` — ошибка или отрицательный вердикт, `2` — BLOCKED, нужно ваше вмешательство. Все коды и статусы — в [Статусах и кодах выхода](../reference/statuses.md).

Прогон сохранился и ждёт. Запишите из вывода две вещи: идентификатор прогона (`run_id`, после `--resume`) и `Plan SHA-256`. Они понадобятся на следующем шаге.

## Как это выглядит в интерактивном терминале

Если запустить `ai-team run` без `< /dev/null` в обычном терминале, остановки с кодом 3 не будет. После delivery-плана контроллер спросит:

```text
Delivery: deployer консолидирует 6 отложенных approval-гейта. Продолжить? [y/N]
```

Ответ `y` подтверждает ровно показанный план, и commit, push и PR пройдут в том же процессе. Любой другой ответ отклоняет delivery и завершает прогон. «Отложенные гейты» — это переходы между этапами, которые в профиле по умолчанию `standard` не спрашивают вас по отдельности, а подтверждаются вместе с планом.

## Что дальше

- [3. Подтвердить план и получить PR](approve.md) — прочитать план, подтвердить его и посмотреть доказательства прогона.
- [Ключевые понятия](../start/concepts.md) — если хочется сначала разобраться в словах из вывода.
