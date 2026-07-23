## Why

Независимый аудит (`INDEPENDENT_AUDIT_2026-07-23.md`, находки 9 и 10) выявил
две связанные проблемы наблюдаемости CLI, каждая воспроизведена вживую:

- `ai-team list` тихо роняет агентов с невалидным `def.yaml`: `Registry.List()`
  добавляет результат `Load(name)` в список только при `err == nil`, ошибка
  нигде не логируется. Пользователь, расширяющий систему через
  `.ai-team/agents/`, не получает вообще никакого сигнала о том, что его
  агент не подхватился.
- Повторный `ai-team run --feature F ...` после успешной доставки `F` тихо
  перезапускает analyst/architect (перезаписывая их артефакты на месте) и
  падает на coder с сообщением «агент coder не создал изменений в коде» —
  которое вне контекста читается как баг агента, а не как «вы уже доставили
  это».

Обе проблемы — не потеря данных и не нарушение гарантий доставки (require-diff
guard и так не даёт доставить пустой diff дважды), а вводящее в заблуждение
или отсутствующее диагностическое сообщение.

## What Changes

- `agent.Registry.List()` возвращает `(agents []*Agent, failures []LoadFailure)`
  вместо одного среза; невалидный, не-shadowing agent definition больше не
  пропадает молча. `ai-team list` печатает failures в stderr.
- Новая `evidence.FindDelivered(runsRoot, feature)` ищет среди прошлых run'ов
  такой, что довёл `feature` до успешной deployer delivery (записанный
  `CommitSHA` и/или `PRURL`). `ai-team run` (только для свежего запуска, не
  для `--retry-from`) печатает non-blocking предупреждение в stderr, если
  находит такой run, — до того, как analyst перезапишет артефакты.
- Предупреждение не блокирует новый run: повторное использование того же
  `--feature` для последующей независимой работы остаётся легитимным
  сценарием, проблема была именно в отсутствии объяснения, а не в том, что
  сценарий нужно запрещать.

## Capabilities

### Modified Capabilities

- `cli-interface`: «Layered agent list» получает scenario про невалидный,
  не-shadowing agent definition; «Run identity and validation» получает
  scenario про предупреждение при повторном run уже доставленной фичи.

## Impact

- `pkg/agent/registry.go` — `List()` signature изменился (единственный
  вызывающий код внутри репозитория — `cmd/ai-team/main.go` — обновлён)
- `cmd/ai-team/main.go` — `cmdList()` печатает failures; `cmdRun()` вызывает
  новую `warnIfAlreadyDelivered`
- `pkg/evidence/query.go` — новый файл, `FindDelivered`
- Никаких изменений в delivery guarantees, mutation policy или verdict
  контракте
