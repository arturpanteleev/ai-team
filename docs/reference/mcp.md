# Сетевой MCP

`ai-team web` отдаёт по адресу `POST /mcp` сервер [Model Context Protocol](https://modelcontextprotocol.io/specification/2026-07-28),
к которому подключаются Claude и Codex. MCP даёт **все продуктовые чтения и действия Web/REST**: проекты, backlog,
задачи (создание, запуск, возобновление, отмена), выбор исполнителя, сдачу результата, запуск агента, повтор гейта,
решения человека, возвраты, пропуски, обход гейта администратором, delivery-план и delivery, шаблон (проверка и
публикация), команду и роли, связь с трекером; а также шаблоны и версии, каталог гейтов, метрики, попытки,
артефакты, логи и события.

MCP ничего не решает сам. Каждый вызов проходит через те же обработчики, middleware и проверку ролей, что и
REST/Web UI, под личностью владельца токена: пользователь видит и может ровно то, что ему доступно в UI. Ответ
инструмента — тот же JSON, что у соответствующего REST-маршрута. Для каждого маршрута сервера есть строка в
[матрице паритета](#матрица-паритета): инструмент либо причина, по которой он не нужен (например, `/api/session`).

> **Проверка на реальных клиентах.** На локальном loopback-сервере (локальный токен) с настоящими клиентами
> проверено: Claude Code 2.1.300 (`claude -p --mcp-config`, заголовок `Authorization: Bearer`, вызов
> `whoami` и `list_projects`) и Codex CLI 0.162.1 (`bearer_token_env_var`, вызов `whoami`). Не проверялось:
> персональные токены и роли облачного режима с этими клиентами (покрыто Go-тестами с клиентом официального SDK),
> подключение через claude.ai connectors (нужен публично доступный HTTPS-адрес) и OAuth (не реализован, см. «Шаг 2»).

## Протокол и транспорт

- Версия MCP: **2026-07-28** (stateless Streamable HTTP), сервер на официальном Go SDK
  `github.com/modelcontextprotocol/go-sdk` v1.8.0 (Apache-2.0; часть старого кода SDK — MIT). Клиенты
  2025-11-25 и старше подключаются через обычный `initialize` без сессии (режим совместимости SDK);
  проверено тестом.
- Один endpoint `POST /mcp`, без `Mcp-Session-Id`. Ответ — `application/json` (не SSE). `GET` и `DELETE` —
  `405`. Уведомление — `202` без тела. Неподдерживаемая версия в `MCP-Protocol-Version` — `400`.
  Тело запроса — до 4 МиБ (результат этапа и YAML шаблона — до 3 МиБ).
- Сервер не шлёт сообщений по своей инициативе, поэтому `listChanged`, подписок и логирования нет.

## Авторизация

Каждый запрос несёт `Authorization: Bearer <токен>`. Cookie-сессия браузера для `/mcp` **не читается**.
Принимаются:

1. **Персональный MCP-токен** (`aitm_…`) — долгоживущий, для Claude/Codex. В облачном режиме.
2. Любой токен, который уже принимает веб-сервер: облачный HMAC-токен (`ai-team auth token`, живёт не дольше
   суток) или локальный токен loopback-сервера (`.ai-team/web.token`).

Любой токен сопоставляется с участником команды так же, как при входе в UI (`resolveTeamPrincipal`): участник
должен быть активен, роли берутся из реестра команды **на каждый вызов**. Смена ролей действует со следующего
запроса, отзыв участника обрывает доступ сразу. Неизвестный, просроченный, отозванный и повреждённый токен дают
один и тот же `401` с одним телом — по ответу нельзя понять, какой токен существует.

### Персональные токены

Выпускает и отзывает только сам пользователь и только для себя (в UI: «Доступ MCP» в боковом меню; доступно
после входа в облачном режиме). Токен показывается один раз; в базе (`mcp_tokens`) лежит SHA-256, метка,
последние 4 символа, срок, время последнего использования и отзыва.

| Запрос | Что делает |
|---|---|
| `GET /api/mcp/tokens` | свои токены (без секрета), `max_active` |
| `POST /api/mcp/tokens` `{"label":"laptop","ttl_days":90}` | выпустить; ответ `201` содержит `token` (один раз). `ttl_days` 1–365, по умолчанию 90; не больше 20 живых токенов на человека |
| `DELETE /api/mcp/tokens/{id}` | отозвать свой токен; чужой или несуществующий — `404` |

Маршруты требуют cookie-сессию и CSRF-токен браузера; MCP-токен ими управлять не может. Выпуск и отзыв
пишутся в журнал команды (`mcp_token_issued`, `mcp_token_revoked`). Токен не работает как REST-учётные данные
и не создаёт браузерных сессий. В локальном режиме (loopback) персональные токены не выпускаются: клиенту
выдаётся локальный токен.

Утёкший токен: отзовите его в UI или `DELETE /api/mcp/tokens/{id}`. Утечка секрета `AI_TEAM_AUTH_SECRET`
требует его смены (см. [Граница безопасности](security.md#веб-дашборд)).

### Проверка Origin и Host

Спецификация требует проверять `Origin`. Запрос **без** `Origin` (CLI, серверные коннекторы) допускается.
Запрос с `Origin` допускается, только если он совпадает с собственным origin сервера или входит в список
`--mcp-allowed-origins` (через запятую, `https://host[:port]`; переменная `AI_TEAM_MCP_ALLOWED_ORIGINS`).
Иначе — `403` до проверки токена. CORS не включается: браузерные клиенты с чужого origin не поддерживаются.
В локальном режиме `Host` обязан быть loopback (защита от DNS rebinding). Прежний `sameOriginMiddleware`
к `/mcp` не применяется: у маршрута свой набор правил. За TLS-прокси передавайте `X-Forwarded-Proto: https`.

## Подключение

Замените `https://ai-team.example.com` на адрес сервера и `aitm_…` на выпущенный токен.

### Claude Code

```bash
claude mcp add --transport http ai-team https://ai-team.example.com/mcp \
  --header "Authorization: Bearer aitm_…"
claude mcp list          # ai-team должен быть Connected
```

Проектный `.mcp.json` (токен берите из переменной окружения, не коммитьте его):

```json
{
  "mcpServers": {
    "ai-team": {
      "type": "http",
      "url": "https://ai-team.example.com/mcp",
      "headers": { "Authorization": "Bearer ${AI_TEAM_MCP_TOKEN}" }
    }
  }
}
```

Разовая проверка без записи конфигурации (так проверялось здесь):
`claude -p "вызови whoami" --mcp-config mcp.json --strict-mcp-config --allowedTools mcp__ai-team__whoami`.

### Codex

`~/.codex/config.toml`:

```toml
[mcp_servers.ai_team]
url = "https://ai-team.example.com/mcp"
bearer_token_env_var = "AI_TEAM_MCP_TOKEN"
```

```bash
export AI_TEAM_MCP_TOKEN=aitm_…
codex exec "вызови ai-team whoami"
```

Без правки конфигурации: `codex exec -c 'mcp_servers.ai_team.url="…/mcp"' -c 'mcp_servers.ai_team.bearer_token_env_var="AI_TEAM_MCP_TOKEN"' "…"`.

### claude.ai connectors

Нужен публично доступный HTTPS-адрес; в поле Request headers укажите `Authorization: Bearer aitm_…`.
Не проверялось. Если Anthropic присылает `Origin`, добавьте его в `--mcp-allowed-origins`.

### Локальный сервер

Для `ai-team web` на loopback используйте локальный токен из `.ai-team/web.token` в том же заголовке и адрес
`http://127.0.0.1:<порт>/mcp`.

## Инструменты и ресурсы

Инструменты чтения помечены `readOnlyHint`, действия — нет (см. [Действия](#действия)). Ошибки приходят как результат с `isError=true` и телом
`{"error","code","status"}`: несуществующий ресурс, нехватка роли и неверные аргументы не раскрывают ничего сверх
того, что вернул бы REST.

| Инструмент | REST-маршрут | Назначение |
|---|---|---|
| `whoami` | `GET /api/auth/me` | личность и роли |
| `list_projects`, `get_project` | `/api/projects` | проекты (`include_archived`) |
| `list_backlog` | `/api/backlog` | идеи, фильтр `project_id` |
| `list_tasks` | `/api/runs` | задачи; фильтры `project_id`, `status`, `stage`, `function`, `mode`; `limit`≤100, `offset` |
| `get_task` | `/api/runs/{run_id}` | полный снимок: попытки, состояние, согласования, результаты, шаблон, delivery-план, доступные действия |
| `get_task_stage_result` | `…/stages/{stage_id}/results/{version}` | версия результата этапа |
| `get_task_workflow`, `get_task_template_version` | `…/workflow`, `…/template-version` | граф и закреплённая версия шаблона |
| `get_task_stage_statuses` | `/api/runs/{id}/stage-statuses` | вычисляемые статусы этапов: статус, готовность, фаза, исполнитель (человек, внутренний или внешний агент), исход, все посещения этапа, что блокирует текущий этап и открытые действия; тот же JSON, что `stage_statuses` в `get_task` |
| `get_task_gates` | `/api/runs/{id}/gates` | реальные гейты каждой версии результата, попытки, обходы администратора, рекомендательные предупреждения и агрегированное разрешение перехода |
| `list_task_events`, `list_events` | `…/events`, `/api/events` | события; курсор `after`/`before`, `order`, `warnings` |
| `get_task_log` | `…/logs/{attempt_id}` | хвост лога попытки (до 64 КиБ) |
| `list_task_artifacts`, `read_task_artifact`, `list_artifact_revisions` | `/api/pipelines/{id}/artifacts`, `…/artifacts/*`, `…/artifact-revisions` | артефакты и ревизии |
| `list_templates`, `get_template`, `list_template_versions` | `/api/templates`, `/api/template`, `/api/template/versions` | шаблон процесса и версии |
| `get_gate_catalog` | `/api/gates/catalog` | каталог гейтов |
| `get_flow_metrics`, `export_flow_metrics_csv` | `/api/metrics/flow`, `/api/metrics/flow.csv` | метрики потока (JSON и CSV) |
| `get_version` | `/api/version` | ревизия сервера |
| `get_task_tracker`, `get_tracker_status`, `get_tracker_sync`, `list_tracker_inbound`, `list_tracker_link_conflicts`, `list_tracker_deliveries` | `/api/runs/{id}/tracker`, `/api/tracker…` | связь задачи с трекером, настройка, состояние опроса, входящие запросы из трекера, конфликты связей, исходящие обновления |
| `list_pipelines`, `get_pipeline`, `read_artifact` | `/api/pipelines…`, `/api/artifacts/*` | прежний вид запусков и общее хранилище артефактов |
| `list_team_members`, `get_team_audit` | `/api/team/…` | команда и роли; только с правом управления командой |
| `get_preflight` | `/api/preflight` | готовность контроллера |

Ресурсы (`resources/read`): `aiteam://tasks/{run_id}`, `aiteam://tasks/{run_id}/stage-statuses`, `aiteam://tasks/{run_id}/stages/{stage_id}/results/{version}`,
`aiteam://projects`, `aiteam://projects/{project_id}`, `aiteam://templates`, `aiteam://template`,
`aiteam://template/versions`, `aiteam://gates/catalog`. Отсутствующий, запрещённый и некорректный ресурс одинаково
дают «Resource not found».

Состояние синхронизации с трекером: `get_task_tracker` (связь, конфликты, последние обновления, входящие запросы, одно
слово о состоянии), `get_tracker_status`, `get_tracker_sync`, `list_tracker_inbound`, `list_tracker_link_conflicts`,
`list_tracker_deliveries`; ошибки интеграции остаются в `list_events` с `warnings=true`.

### Размер ответов и пагинация

- Списки без собственной пагинации в REST (`list_projects`, `list_backlog`, `list_task_artifacts`,
  `list_template_versions`, `list_team_members`, `get_team_audit`) принимают `limit` (1–200, по умолчанию 50) и
  `offset` и добавляют `total`, `limit`, `offset`, `has_more`, `next_offset`; остальные поля — как в REST.
  `list_task_artifacts` возвращает `{"artifacts":[…], …}` вместо голого массива.
- `list_tasks` и события используют собственные `limit`/`offset` и курсор REST.
- Любой результат больше 256 КиБ режется по границе UTF-8 и возвращается конвертом
  `{"truncated":true,"total_bytes":…,"byte_offset":…,"content":"…","next_byte_offset":…}`; продолжение — тот же
  вызов с `byte_offset=next_byte_offset`. Любой инструмент принимает `byte_offset`.
- Артефакты возвращаются как текст; нетекстовое содержимое не отдаётся.

## Действия

Действие — это инструмент, который вызывает тот же `POST`/`PUT`/`PATCH`/`DELETE`-маршрут, что кнопка в Web UI.
Он выполняется под личностью владельца токена; ни один инструмент не принимает `actor_id`. Проверка роли, функции
этапа, права администратора, самоподтверждения, точного хеша и причины делается теми же обработчиками, что для
браузера, поэтому клиент без права получает тот же `403`/`409`, и действие не доходит до контроллера.

| Группа | Инструменты | Что нужно |
|---|---|---|
| Проекты | `create_project`, `update_project`, `archive_project`, `unarchive_project` | право управлять проектами (PO, архитектор) |
| Backlog | `create_backlog_item`, `set_backlog_item_status` | право запускать задачи (PO, архитектор); `project_id` обязателен |
| Задача | `start_task` (`project_id` обязателен), `resume_task`, `cancel_task` | PO/архитектор; PO/архитектор/разработчик/QA; PO/релиз-менеджер |
| Исполнитель этапа | `set_stage_executor`, `change_task_executor`, `take_stage`, `run_stage_agent` | функция этапа; запуск агента записывает запустившего автором этапа |
| Результат | `submit_stage_result`, `create_artifact_revision`, `answer_stage_grill`, `submit_stage_grill_version`, `complete_stage_grill` | функция этапа; правка артефакта — PO/архитектор/ревьюер/QA |
| Подтверждения | `confirm_stage`, `decide_approval` | роль подтверждения, **точный `subject_hash`**, запрет самоподтверждения |
| Возврат, пропуск, обход | `return_stage`, `skip_stage`, `override_stage_gate` | причина обязательна; обход — только системный администратор |
| Гейты | `retry_stage_gate`, `retry_gate_attempt` | функция этапа; только для `error`/`timeout` той же версии результата |
| Delivery | `approve_delivery_plan`, `retry_delivery` | `plan_hash` — SHA-256 утверждаемого плана; PO/релиз-менеджер |
| Шаблон | `validate_template` (только чтение), `publish_template` | PO/архитектор; `expected_version` защищает от перезаписи |
| Команда | `invite_team_member`, `set_team_member_roles`, `revoke_team_member` | право управлять командой; право администратора выдаёт и снимает только администратор |
| Трекер | `set_task_tracker_link`, `remove_task_tracker_link`, `retry_task_tracker_deliveries`, `retry_task_tracker_delivery`, `sync_tracker_now`, `dismiss_tracker_inbound` | право связи с трекером (PO, архитектор); входящее изменение из трекера двигает задачу только через контроллер |

Что MCP намеренно **не** делает: токены (`/api/mcp/tokens`) управляются только из браузерной сессии с CSRF — MCP-токен
не может выпускать, перечислять и отзывать токены (маршрут отвечает `403` на любой запрос, пришедший из MCP);
браузерная cookie-сессия и вход (`/api/session`, SSO), активация приглашения (`/api/team/activate`).

### Ключ запроса и повтор доставки

Каждый `POST`-инструмент требует `idempotency_key` (1–128 видимых символов, обычно UUID); у `PUT`, `PATCH` и `DELETE`
он необязателен (они заменяют или удаляют конкретный объект), у `validate_template` не нужен (ничего не меняет).

- Первая доставка занимает ключ, выполняет действие и сохраняет ответ. Повтор того же вызова с тем же ключом и
  теми же аргументами возвращает **тот же ответ без выполнения** (в `_meta` результата `idempotent_replay: true`):
  второго решения, запуска, возврата или delivery не будет.
- Тот же ключ для другого вызова или с другими аргументами — ошибка `idempotency_conflict` (`409`), параллельный
  повтор — `idempotency_in_progress`. Ключ принадлежит человеку: ключ другого человека — другое пространство.
- Ответ с кодом `5xx` ключ не занимает (действие могло не произойти): повторите с тем же ключом. Отказ `4xx`
  запоминается; после исправления причины используйте новый ключ.
- Секреты, которые показываются один раз (`activation_token` в `invite_team_member`), в сохранённый для повторов
  ответ не попадают; повтор возвращает исход без секрета и не создаёт второго приглашения.
- Отдельно решение человека защищено сервисом решений: повтор того же человека, действия, хеша и роли распознаётся как
  replay и не считается вторым голосом (событие `human_gate_decision_replayed`), даже с новым ключом.

### Подтверждения и хеш

`confirm_stage` и `decide_approval` требуют `subject_hash` — SHA-256 версии результата (или субъекта согласования),
которую вы просмотрели; его видно в `get_task` (`approvals[].subject_hash`). Сервер не подставляет хеш за клиента, как
делает для устаревшего UI-клиента. Устаревший хеш — `409 subject_hash_mismatch`. `approve_delivery_plan` требует
`plan_hash` плана, `override_stage_gate` — непустую `reason`. `decide_approval` только записывает решение: чтобы
перевести задачу после подтверждённого перехода, вызовите `resume_task` (`confirm_stage` записывает и возобновляет сразу).

### Длинные действия

`start_task`, `resume_task`, `run_stage_agent`, `confirm_stage`, `override_stage_gate` и другие возвращают сразу (`202`)
с идентификатором задачи; ход работы читается отдельно: `get_task` (фаза, этап, ожидающие согласования, попытки),
`get_task_gates` (отчёты гейтов), `list_task_events` (курсор `after`), `get_task_log`. Опрашивайте их, пока у задачи не
появится ожидающее согласование или не завершится фаза.

### Аудит и канал

Каждое успешное действие через MCP оставляет запись `mcp_action` (`channel: "mcp"`, `tool`, `actor_id`, `token_id`,
`client` — User-Agent клиента, `idempotency_key`, статус) в журнале задачи; действия без задачи (проекты, backlog,
шаблон, команда) — в журнале команды (`get_team_audit`). Аргументы не записываются. Решения человека дополнительно несут
`channel: "mcp"` и `request_key` в своём событии `human_gate_decided`.

## Матрица паритета

Это машинно проверяемый перечень **каждого маршрута роутера**. Тест `TestMCPParityMatrix` обходит реальный
`chi`-роутер (`chi.Walk`) в трёх конфигурациях (облачный режим, SSO, раздача фронтенда) и падает, если:
маршрут есть, а строки в матрице (`pkg/web/mcp_parity.go`) нет; строка называет несуществующий маршрут или
инструмент; инструмент вызывает не тот метод или путь; инструмент не относится ни к одной строке; исключение без
причины. `TestMCPParityMatrixIsInTheReferenceDocument` проверяет, что эта страница перечисляет все маршруты и
инструменты. Новый маршрут (например, будущее чтение статусов или ручной `sync now`) поэтому нельзя добавить без
решения о его MCP-эквиваленте.

### Маршруты с инструментом

| Метод | Маршрут | Инструмент |
|---|---|---|
| GET | `/api/auth/me` | `whoami` |
| GET | `/api/version` | `get_version` |
| GET | `/api/preflight` | `get_preflight` |
| GET | `/api/projects` | `list_projects` |
| POST | `/api/projects` | `create_project` |
| GET | `/api/projects/{projectID}` | `get_project` |
| PATCH | `/api/projects/{projectID}` | `update_project` |
| POST | `/api/projects/{projectID}/archive` | `archive_project` |
| POST | `/api/projects/{projectID}/unarchive` | `unarchive_project` |
| GET | `/api/backlog` | `list_backlog` |
| POST | `/api/backlog` | `create_backlog_item` |
| POST | `/api/backlog/{itemID}/status` | `set_backlog_item_status` |
| GET | `/api/runs` | `list_tasks` |
| GET | `/api/runs/{runID}` | `get_task` |
| GET | `/api/runs/{runID}/events` | `list_task_events` |
| GET | `/api/runs/{runID}/gates` | `get_task_gates` |
| GET | `/api/runs/{runID}/stages/{stageID}/results/{version}` | `get_task_stage_result` |
| GET | `/api/runs/{runID}/workflow` | `get_task_workflow` |
| GET | `/api/runs/{runID}/template-version` | `get_task_template_version` |
| GET | `/api/runs/{runID}/logs/{attemptID}` | `get_task_log` |
| GET | `/api/runs/{runID}/artifacts/*` | `read_task_artifact` |
| GET | `/api/runs/{runID}/artifact-revisions` | `list_artifact_revisions` |
| GET | `/api/runs/{runID}/tracker` | `get_task_tracker` |
| GET | `/api/events` | `list_events` |
| GET | `/api/metrics/flow` | `get_flow_metrics` |
| GET | `/api/metrics/flow.csv` | `export_flow_metrics_csv` |
| GET | `/api/pipelines` | `list_pipelines` |
| GET | `/api/pipelines/{id}` | `get_pipeline` |
| GET | `/api/pipelines/{id}/artifacts` | `list_task_artifacts` |
| GET | `/api/artifacts/*` | `read_artifact` |
| POST | `/api/runs` | `start_task` |
| POST | `/api/runs/{runID}/resume` | `resume_task` |
| POST | `/api/runs/{runID}/cancel` | `cancel_task` |
| POST | `/api/runs/{runID}/executor` | `change_task_executor` |
| POST | `/api/runs/{runID}/delivery/retry` | `retry_delivery` |
| POST | `/api/runs/{runID}/plan/approve` | `approve_delivery_plan` |
| POST | `/api/runs/{runID}/approvals/{approvalID}/decisions` | `decide_approval` |
| POST | `/api/runs/{runID}/artifact-revisions` | `create_artifact_revision` |
| POST | `/api/runs/{runID}/gate-attempts/{attemptID}/retry` | `retry_gate_attempt` |
| POST | `/api/runs/{runID}/stages/{stageID}/executor` | `set_stage_executor` |
| POST | `/api/runs/{runID}/stages/{stageID}/take` | `take_stage` |
| POST | `/api/runs/{runID}/stages/{stageID}/run-agent` | `run_stage_agent` |
| POST | `/api/runs/{runID}/stages/{stageID}/results` | `submit_stage_result` |
| POST | `/api/runs/{runID}/stages/{stageID}/submit` | `submit_stage_result` (второе написание того же обработчика) |
| POST | `/api/runs/{runID}/stages/{stageID}/confirm` | `confirm_stage` |
| POST | `/api/runs/{runID}/stages/{stageID}/skip` | `skip_stage` |
| POST | `/api/runs/{runID}/stages/{stageID}/return` | `return_stage` |
| POST | `/api/runs/{runID}/stages/{stageID}/override` | `override_stage_gate` |
| POST | `/api/runs/{runID}/stages/{stageID}/gates/{gate}/retry` | `retry_stage_gate` |
| POST | `/api/runs/{runID}/stages/{stageID}/grill/answer` | `answer_stage_grill` |
| POST | `/api/runs/{runID}/stages/{stageID}/grill/new-version` | `submit_stage_grill_version` |
| POST | `/api/runs/{runID}/stages/{stageID}/grill/done` | `complete_stage_grill` |
| GET | `/api/templates` | `list_templates` |
| GET | `/api/template` | `get_template` |
| GET | `/api/template/versions` | `list_template_versions` |
| GET | `/api/gates/catalog` | `get_gate_catalog` |
| POST | `/api/template/validate` | `validate_template` |
| POST | `/api/template/publish` | `publish_template` |
| GET | `/api/team/members` | `list_team_members` |
| GET | `/api/team/audit` | `get_team_audit` |
| POST | `/api/team/invitations` | `invite_team_member` |
| PATCH | `/api/team/members/{actorID}/roles` | `set_team_member_roles` |
| DELETE | `/api/team/members/{actorID}` | `revoke_team_member` |
| GET | `/api/tracker` | `get_tracker_status` |
| GET | `/api/tracker/conflicts` | `list_tracker_link_conflicts` |
| GET | `/api/tracker/deliveries` | `list_tracker_deliveries` |
| GET | `/api/tracker/sync` | `get_tracker_sync` |
| GET | `/api/tracker/inbound` | `list_tracker_inbound` |
| POST | `/api/tracker/sync` | `sync_tracker_now` |
| POST | `/api/runs/{runID}/tracker/inbound/{changeID}/dismiss` | `dismiss_tracker_inbound` |
| PUT | `/api/runs/{runID}/tracker/link` | `set_task_tracker_link` |
| DELETE | `/api/runs/{runID}/tracker/link` | `remove_task_tracker_link` |
| POST | `/api/runs/{runID}/tracker/retry` | `retry_task_tracker_deliveries` |
| POST | `/api/runs/{runID}/tracker/deliveries/{eventID}/retry` | `retry_task_tracker_delivery` |

### Маршруты без инструмента (исключения)

Функцию этих маршрутов выполняет транспорт MCP либо она запрещена для MCP намеренно.

| Метод | Маршрут | Почему инструмента нет |
|---|---|---|
| все | `/mcp` | сам MCP-endpoint: через него вызываются все инструменты |
| GET | `/ws` | push-канал браузера; MCP stateless и без сообщений по инициативе сервера, то же читается опросом `list_events` / `list_task_events` с курсором |
| GET | `/*` | статические файлы фронтенда (только если фронтенд собран), не API |
| GET | `/api/auth/config` | публичная справка для входа в браузере; MCP-клиент уже аутентифицирован токеном на каждом вызове, личность — `whoami` |
| GET | `/api/session` | обменивает Bearer-токен на cookie-сессию и CSRF-токен; у MCP нет cookie-сессии, а Bearer-токен аутентифицирует каждый вызов и для действий **заменяет CSRF** (внутренний запрос несёт CSRF-токен этой личности) |
| DELETE | `/api/session` | завершает cookie-сессию; у MCP её нет, доступ прекращается отзывом токена в UI, истечением срока или отзывом участника |
| GET | `/api/auth/oidc/login`, `/api/auth/oidc/callback` | браузерные редиректы входа через SSO (только при включённом SSO) |
| POST | `/api/team/activate` | приглашённый активирует себя до того, как станет участником; MCP не принимает не участника и не создаёт команду |
| GET, POST | `/api/mcp/tokens` | выпуск и список личных токенов: только браузерная сессия с CSRF; MCP-токен не управляет токенами (маршрут отвечает `403` на запрос из MCP) |
| DELETE | `/api/mcp/tokens/{tokenID}` | отзыв токена: то же |

## Шаг 2: OAuth (не реализован)

Статический Bearer достаточен для Claude Code и Codex. OAuth по RFC 9728 (protected resource metadata,
`WWW-Authenticate: Bearer resource_metadata=…`) и внешний authorization server понадобятся для коннекторов,
которые не умеют статические заголовки. SDK уже содержит `auth.ProtectedResourceMetadataHandler`; нужно
выбрать authorization server (например, текущий OIDC-провайдер) и сопоставить его subject с участником команды
тем же `resolveTeamPrincipal`. Транспорт менять не придётся.

## Для разработчиков

Инструменты — декларативный реестр `mcpOperation` (`pkg/web/mcp_read_tools.go`, `pkg/web/mcp_action_tools.go`): REST-путь,
метод, параметры (путь, запрос или тело JSON), схема. Вызов выполняется внутренним запросом через тот же `chi`-роутер с
личностью токена в контексте (`requestSession` читает её раньше cookie; ключ контекста неэкспортирован); для действия
внутренний запрос несёт CSRF-токен этой же личности. Поэтому переходы состояний, RBAC и серверные проверки не
дублируются. Ключ запроса и аудит `mcp_action` — единственное, что добавляет слой MCP (`mcp_registry.go`). Новая запись
реестра без строки в `mcp_parity.go` (и наоборот) не пройдёт тесты. Решения человека получают канал `mcp` из контекста
вызова (`decisionOrigin`), а не из запроса.
