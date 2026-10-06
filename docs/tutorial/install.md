# 1. Установка

На этой странице вы поставите `ai-team` и убедитесь, что он запускается. Дальше по учебнику вы возьмёте маленький Go-проект greeter и проведёте через `ai-team` одну задачу: от описания до pull request.

> [!LEARN]
> - что понадобится для учебника и для настоящей работы
> - три способа установить `ai-team`: готовый бинарник, `go install`, сборка из исходников
> - как проверить подпись релиза
> - как убедиться, что команда работает

## Что понадобится

| Что | Зачем | Проверка |
|---|---|---|
| Go 1.26.5 или новее | собрать `ai-team` и запускать проверки Go-проекта | `go version` |
| Git | кандидат и delivery в `ai-team` работают через Git | `git --version` |
| Bash | агенты-заглушки в учебнике — Bash-скрипт | `bash --version` |
| [`gh`](https://cli.github.com), авторизованный | открывать pull request при настоящей delivery | `gh auth status` |

Минимальная версия Go указана в [go.mod](../../go.mod). Учебник проверен на macOS и Linux.

Для шагов 2 и 3 не нужны ни API-ключи, ни модель, ни настоящий `gh`: вместо них будут заглушки. `gh` и модель понадобятся на шаге 4, когда вы подключите настоящий runtime.

## Выбрать способ установки

| Способ | Когда подходит |
|---|---|
| [Готовый бинарник](#вариант-а-готовый-бинарник) | нужна проверенная релизная версия, Go для сборки не нужен |
| [`go install`](#вариант-б-go-install) | Go уже стоит, нужна последняя релизная версия |
| [Сборка из исходников](#вариант-в-собрать-из-исходников) | нужен актуальный `master` или вы идёте по учебнику |

> [!TIP]
> Для учебника удобнее всего вариант В. Заглушки агентов лежат в репозитории `ai-team`, поэтому клон понадобится в любом случае. Если вы поставили бинарник другим способом, всё равно склонируйте репозиторий: `git clone https://github.com/arturpanteleev/ai-team.git`.

## Вариант А: готовый бинарник

Для каждого тега `v*` публикуется релиз: архивы под darwin/linux × amd64/arm64, файл `sha256sums.txt` и подписи cosign — файл `<архив>.cosign.bundle` к каждому архиву и к `sha256sums.txt`. Подпись ставит job `.github/workflows/release.yaml` этого репозитория на этом теге, запись о ней лежит в публичном журнале Rekor.

Подмену выявляет не наличие подписи, а её проверка. Для неё нужен cosign версии 2.4.2 или новее (рекомендуется 3.x): `cosign version`. Установка — по [docs.sigstore.dev](https://docs.sigstore.dev/cosign/system_config/installation/).

> [!IMPORTANT]
> `v0.2.0` и всё, что опубликовано раньше, подписей не имеет: команды ниже упадут с «no such file or directory». Это не признак подмены, просто такой релиз этим способом не проверить. У всего, что опубликовано после `v0.2.0`, подписи обязаны быть. Если у нового релиза нет `sha256sums.txt` или `.cosign.bundle` хотя бы к одному архиву, не доверяйте его файлам. Список файлов релиза: `gh release view <tag> --repo arturpanteleev/ai-team --json assets`.

Сохраните блок в файл `install.sh`, подставьте тег и запустите `bash install.sh`. Строка `set -euo pipefail` обязательна: без неё бинарник установится даже после проваленной проверки. Не вставляйте блок в интерактивный шелл: `set -e` закроет окно вместе с сообщением об ошибке.

```bash
set -euo pipefail

VERSION=vX.Y.Z                # тег нужного релиза
REPO=arturpanteleev/ai-team
IDENTITY="https://github.com/$REPO/.github/workflows/release.yaml@refs/tags/$VERSION"
ISSUER=https://token.actions.githubusercontent.com

# Платформа определяется автоматически. Подставлять имя архива руками не стоит:
# чужой, но подлинный архив пройдёт обе проверки, и в /usr/local/bin окажется
# бинарник не для вашей платформы.
case "$(uname -s)" in
  Darwin) OS=darwin; sha256check() { shasum -a 256 -c -; } ;;
  Linux)  OS=linux;  sha256check() { sha256sum -c -; } ;;
  *) echo "неподдерживаемая ОС: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) echo "неподдерживаемая архитектура: $(uname -m)" >&2; exit 1 ;;
esac
ARCHIVE="ai-team-${OS}-${ARCH}.tar.gz"

gh release download "$VERSION" --repo "$REPO" \
  --pattern "$ARCHIVE*" \
  --pattern 'sha256sums.txt*'

# 1. Подлинность: подпись сделана этим workflow на этом теге.
cosign verify-blob \
  --bundle sha256sums.txt.cosign.bundle \
  --certificate-identity "$IDENTITY" \
  --certificate-oidc-issuer "$ISSUER" \
  sha256sums.txt

# 2. Целостность: сверяем ровно скачанный архив с уже проверенным sha256sums.txt.
grep " $ARCHIVE\$" sha256sums.txt | sha256check

# 3. Установка — только если обе проверки прошли.
tar -xzf "$ARCHIVE"
mv "${ARCHIVE%.tar.gz}" /usr/local/bin/ai-team
```

Если `cosign verify-blob` или сверка сумм вернули ненулевой код на подписанном релизе, не запускайте этот бинарник.

> [!DEEPDIVE] Почему блок устроен именно так
> - **`--certificate-identity` и `--certificate-oidc-issuer`.** Без них cosign подтвердит только то, что подпись кем-то сделана, но не кем. Identity — путь к релизному workflow плюс ref тега. Если workflow переименуют, сверьтесь с актуальным [release.yaml](../../.github/workflows/release.yaml).
> - **`grep` и `-c -` вместо `--ignore-missing`.** В `sha256sums.txt` перечислены все четыре платформы, а скачана одна. На macOS `/sbin/sha256sum -c --ignore-missing` возвращает 0, когда не проверено ни одного файла, — ложно-зелёный результат, если архив не скачался.
> - **`pipefail`.** Если `grep` ничего не нашёл, на вход проверке приходит пустота, а Apple-овский `sha256sum -c -` на пустом вводе тоже возвращает 0. Код конвейера берётся от `grep` только при `pipefail`, поэтому эту строку нельзя выносить из блока.
> - **`gh release download` возвращает 0**, даже если какой-то шаблон ничего не нашёл. Нехватка файлов всплывёт только на проверке.
>
> Отдельный архив можно проверить и напрямую, у него свой bundle:
>
> ```bash
> cosign verify-blob \
>   --bundle "$ARCHIVE.cosign.bundle" \
>   --certificate-identity "$IDENTITY" \
>   --certificate-oidc-issuer "$ISSUER" \
>   "$ARCHIVE"
> ```

## Вариант Б: go install

```bash
go install github.com/arturpanteleev/ai-team/cmd/ai-team@latest
```

Команда ставит последний **тег**, он может отставать от `master`. Бинарник окажется в `$(go env GOPATH)/bin` — этот каталог должен быть в `PATH`.

## Вариант В: собрать из исходников

```bash
git clone https://github.com/arturpanteleev/ai-team.git
cd ai-team
go build -o bin/ai-team ./cmd/ai-team
export PATH="$PWD/bin:$PATH"
```

`export` действует только в текущем окне терминала. Чтобы `ai-team` был доступен всегда, добавьте каталог `bin` в `PATH` в настройках шелла или скопируйте бинарник в каталог, который уже там есть.

Для воспроизводимости запишите, из какого коммита собран бинарник: `git rev-parse --short HEAD`.

## Проверить установку

```bash
ai-team version
```

Сборка из исходников и `go install` печатают `dev`, релизный бинарник — свой тег:

```text
dev
```

Полный список команд — `ai-team help` или [Команды CLI](../reference/cli.md).

## Что дальше

- [2. Первая задача без модели](first-run.md) — подготовить проект greeter и прогнать задачу на агентах-заглушках.
- [Подходит ли вам](../start/fit.md) — если вы ещё решаете, нужен ли вам `ai-team`.
