# Сетевой MCP

`ai-team web` отдаёт по адресу `POST /mcp` сервер [Model Context Protocol](https://modelcontextprotocol.io/specification/2026-07-28),
к которому подключаются Claude и Codex. В этой версии MCP **только читает**: проекты, backlog, задачи,
шаблоны и версии, каталог гейтов, метрики, попытки, артефакты, логи, события, delivery-план и команду.
Действия (запуск, решения, возвраты, обходы, публикация шаблона) добавляет следующий шаг; транспорт и
авторизация для них уже готовы.

MCP ничего не решает сам. Каждый вызов проходит через те же обработчики, middleware и проверку ролей, что и
REST/Web UI, под личностью владельца токена: пользователь видит ровно то, что ему доступно в UI. Ответ
инструмента — тот же JSON, что у соответствующего REST-маршрута.

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
  Тело запроса — до 1 МиБ.
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

Все инструменты помечены `readOnlyHint`. Ошибки приходят как результат с `isError=true` и телом
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
| `get_task_gates` | `/api/runs/{id}/gates` | реальные гейты каждой версии результата, попытки, обходы администратора, рекомендательные предупреждения и агрегированное разрешение перехода |
| `list_task_events`, `list_events` | `…/events`, `/api/events` | события; курсор `after`/`before`, `order`, `warnings` |
| `get_task_log` | `…/logs/{attempt_id}` | хвост лога попытки (до 64 КиБ) |
| `list_task_artifacts`, `read_task_artifact`, `list_artifact_revisions` | `/api/pipelines/{id}/artifacts`, `…/artifacts/*`, `…/artifact-revisions` | артефакты и ревизии |
| `list_templates`, `get_template`, `list_template_versions` | `/api/templates`, `/api/template`, `/api/template/versions` | шаблон процесса и версии |
| `get_gate_catalog` | `/api/gates/catalog` | каталог гейтов |
| `get_flow_metrics` | `/api/metrics/flow` | метрики потока |
| `list_team_members`, `get_team_audit` | `/api/team/…` | команда и роли; только с правом управления командой |
| `get_preflight` | `/api/preflight` | готовность контроллера |

Ресурсы (`resources/read`): `aiteam://tasks/{run_id}`, `aiteam://tasks/{run_id}/stages/{stage_id}/results/{version}`,
`aiteam://projects`, `aiteam://projects/{project_id}`, `aiteam://templates`, `aiteam://template`,
`aiteam://template/versions`, `aiteam://gates/catalog`. Отсутствующий, запрещённый и некорректный ресурс одинаково
дают «Resource not found».

Состояние Jira-синхронизации: отдельного REST-маршрута пока нет (его добавит шаг трекеров). Сейчас видны ссылки
задачи (`external_links` в `get_task`) и ошибки интеграции (`list_events` с `warnings=true`, тип
`integration_error`).

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

## Шаг 2: OAuth (не реализован)

Статический Bearer достаточен для Claude Code и Codex. OAuth по RFC 9728 (protected resource metadata,
`WWW-Authenticate: Bearer resource_metadata=…`) и внешний authorization server понадобятся для коннекторов,
которые не умеют статические заголовки. SDK уже содержит `auth.ProtectedResourceMetadataHandler`; нужно
выбрать authorization server (например, текущий OIDC-провайдер) и сопоставить его subject с участником команды
тем же `resolveTeamPrincipal`. Транспорт менять не придётся.

## Для разработчиков

Инструменты — декларативный реестр `mcpOperation` (`pkg/web/mcp_read_tools.go`): REST-путь, параметры, схема.
Вызов выполняется внутренним запросом через тот же `chi`-роутер с личностью токена в контексте
(`requestSession` читает её раньше cookie; ключ контекста неэкспортирован). Поэтому переходы состояний,
RBAC, идемпотентность и аудит не дублируются. Новые чтения добавляются одной записью; действия (S13) —
записями с методом `POST`, ключом запроса и CSRF внутреннего запроса.
