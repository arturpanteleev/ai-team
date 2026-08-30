# Демо: `gate → bundle → verify`

Цель — показать, что ai-team проверяет изменения **детерминированно**, без
LLM и без ручных допущений, на **не-Go** репозитории, и что результат можно
перепроверить самодостаточно. Требование к этому разделу: три внешних
человека должны пройти демо без вопросов — поэтому ниже только команды и их
ожидаемый результат.

## Локально (5 минут)

Требования: `bash`, `git`, `go` (1.26+). Никаких API-ключей и `.ai-team` нет.

```bash
bash docs/demo/run-demo.sh
```

Скрипт:

1. Собирает `ai-team` из текущей ревизии и печатает её SHA.
2. Создаёт в `/tmp` изолированный Python-репозиторий `demo-app` с
   `gate.yaml` (diff-policy `test_modify: required` + typed check `junit-xml`).
3. Прогоняет три сценария от общего base-коммита:

   | Сценарий | Что в diff | Ожидаемый exit | Причина |
   |---|---|---|---|
   | `pass-fix` | source + сменяющий тест, зелёный JUnit-отчёт | `0` | всё согласовано |
   | `policy-fail` | source-правка **без** тестов | `1` | `test_modify: required` |
   | `junit-fail` | тест есть, но failure в XML | `1` | отчёт авторитетнее exit code |

4. Для каждого сценария запускает `ai-team verify <bundle>`: bundle проверяется
   самодостаточно (по каноническому `index.json` и дигестам records, без
   исходного repo и без `.ai-team`). FAIL-bundle тоже верифицируется — провал
   отражается в вердикте, а не в целостности записей.

Попробуйте нарушить bundle вручную (например, `echo x >> bundle-pass/checks/*.json`)
и повторите `ai-team verify bundle-pass` — команда откажет, а если изменить
`index.json` — отвергнет «лишний/несовпадающий record».

## В CI одним version-pinned файлом

[`ci-gate-demo.yaml`](ci-gate-demo.yaml) — единственный файл, который нужно
положить в репозиторий. Он:

1. Получает `merge-base` PR-ветки (base).
2. Собирает `ai-team` из зафиксированной ревизии `AI_TEAM_SHA` (version-pinned;
   до появления релизных тегов — это обязательный SHA, а не "latest").
3. Запускает `ai-team gate --base <merge-base> --candidate HEAD --out bundle` —
   вердикт иного exit code останавливает job (FAIL — отказ PR, BLOCKED —
   инфраструктурный сбой).
4. В любом случае (`if: always()`) выполняет `ai-team verify bundle`
   (самодостаточная проверка в том же job) и загружает bundle артефактом.

### Как обновлять версии в CI-файле

- `AI_TEAM_SHA` — фиксируйте после каждого merge в ai-team.
- SHA-пины `actions/*` и `upload-artifact` — фиксируйте при обновлении.
- Когда появится GitHub-релиз (задача OPS-1), замените сборку из SHA на
  скачивание pinned `vX.Y.Z` бинарника; пока честно используем SHA.

## Порядок: gate ≠ run

Это демо показывает **gate** — лёгкий слой. Полный **run** (LLM-агенты,
approvals, evidence, delivery) описан в
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) и README. Gate можно использовать
отдельно; и gate, и run опираются на одни и те же typed checks.