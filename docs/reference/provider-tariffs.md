# Официальные цены провайдеров

Готовые записи для `usage.tariffs` (см. [Тарифы провайдера](config.md#тарифы-провайдера)):
цены Anthropic и OpenAI, снятые с официальных страниц провайдеров, с датой и источником.
ai-team не применяет их сам — таблица тарифов в конфиге по умолчанию пуста; блок
YAML в конце страницы копируется в `.ai-team/config.yaml` проекта.

- **Дата снятия цен:** 2026-10-13. На страницах провайдеров дата обновления цен не
  указана, поэтому `effective_date` во всех записях — дата снятия.
- **Anthropic:** <https://claude.com/pricing> — официальная страница цен (на неё ведёт
  redirect с `anthropic.com/pricing`).
- **OpenAI:** <https://developers.openai.com/api/docs/pricing>.
- **Единицы:** USD за 1M токенов.

Цены меняются. Когда провайдер публикует новый прайс, добавьте новые записи с новой
`effective_date`, а прежние не удаляйте: попытки, завершённые до этой даты, остаются
оценены по прежнему тарифу.

## Anthropic

Текущие модели, стандартный тариф, запись в кеш — 5-минутный кеш.

| Модель | `model` | Вход | Выход | Запись в кеш (5 мин) | Чтение из кеша |
|---|---|---|---|---|---|
| Claude Fable 5.1 | `claude-fable-5-1` | 10 | 50 | 12.50 | 0.25 |
| Claude Opus 5.5 | `claude-opus-5-5` | 4 | 20 | 5 | 0.20 |
| Claude Sonnet 5.5 | `claude-sonnet-5-5` | 2 | 10 | 2.50 | 0.10 |

**Claude Haiku 5.5 (`claude-haiku-5-5`) не включён.** Его цена ступенчатая по длине
промпта:

| Длина промпта | Вход | Выход |
|---|---|---|
| до 100K токенов включительно | 0.10 | 0.50 |
| больше 100K токенов | 0.50 | 2.50 |

Запись
`usage.tariffs` задаёт одну цену на модель и ступени по длине промпта не поддерживает,
поэтому любая одна ставка давала бы неверную стоимость части попыток. Попытки на
Haiku 5.5 показываются как `нет данных` с причиной «тариф не задан» — это честнее
заниженной или завышенной суммы.

Не учтены — отдельных записей для этих режимов нет; модель в них та же, поэтому попытка
в таком режиме будет оценена по стандартной цене, и сумма для неё будет неточной:

- **1-часовой кеш** — на странице цен не указан; в таблице только запись в 5-минутный кеш.
- **Fast mode** — 2× к стандартной цене.
- **US-only инференс** — 1.1× к стандартной цене.
- **Batch API** — скидка 50%.

## OpenAI

Текущий стандартный тариф, промпты до 272K входных токенов включительно. «Кеш» — цена
кешированных входных токенов.

| `model` | Вход | Кешированный вход | Выход |
|---|---|---|---|
| `gpt-6-astra` | 10 | 1 | 50 |
| `gpt-6.1-sol` | 2 | 0.10 | 10 |
| `gpt-6-luna` | 0.10 | 0.01 | 0.50 |
| `gpt-6-sol` | 2 | 0.20 | 10 |
| `gpt-5.6-sol` | 4 | 0.40 | 20 |
| `gpt-5.6-terra` | 2 | 0.20 | 12 |
| `gpt-5.6-luna` | 0.20 | 0.02 | 1.20 |
| `gpt-5.5` | 5 | 0.50 | 30 |
| `gpt-5.5-pro` | 30 | — | 180 |
| `gpt-5.4` | 2.50 | 0.25 | 15 |
| `gpt-5.4-mini` | 0.75 | 0.075 | 4.50 |
| `gpt-5.4-nano` | 0.20 | 0.02 | 1.25 |
| `gpt-5.4-pro` | 30 | — | 180 |
| `gpt-5.2` | 1.75 | 0.175 | 14 |
| `gpt-5.2-pro` | 21 | — | 168 |
| `gpt-5.1` | 1.25 | 0.125 | 10 |
| `gpt-5` | 1.25 | 0.125 | 10 |
| `gpt-5-mini` | 0.25 | 0.025 | 2 |
| `gpt-5-nano` | 0.05 | 0.005 | 0.40 |
| `gpt-5-pro` | 15 | — | 120 |

«—» — у модели нет цены кешированного входа. В записи для неё нет
`cached_input_per_mtok`, поэтому попытка с кешированными токенами не оценивается и
показывается как `нет данных`, а не по цене обычного входа.

Не учтены:

- **длинный контекст** — цены для промптов длиннее 272K входных токенов;
- **Batch, Flex, Fast и Realtime** — отдельные режимы со своими ценами;
- **изображения и аудио** — токены и цены не текстовых модальностей.

## Блок для usage.tariffs

Блок проверяется тестом `docs/docs_test.go`: он загружается через `pkg/config` и
проходит ту же валидацию, что и конфиг проекта.

```yaml
usage:
  tariffs:
    # Anthropic — https://claude.com/pricing, снято 2026-10-13
    - model: claude-fable-5-1
      input_per_mtok: 10
      output_per_mtok: 50
      cached_input_per_mtok: 0.25
      cache_write_per_mtok: 12.50
      currency: USD
      effective_date: "2026-10-13"
      source: https://claude.com/pricing
    - model: claude-opus-5-5
      input_per_mtok: 4
      output_per_mtok: 20
      cached_input_per_mtok: 0.20
      cache_write_per_mtok: 5
      currency: USD
      effective_date: "2026-10-13"
      source: https://claude.com/pricing
    - model: claude-sonnet-5-5
      input_per_mtok: 2
      output_per_mtok: 10
      cached_input_per_mtok: 0.10
      cache_write_per_mtok: 2.50
      currency: USD
      effective_date: "2026-10-13"
      source: https://claude.com/pricing
    # OpenAI — https://developers.openai.com/api/docs/pricing, снято 2026-10-13
    - model: gpt-6-astra
      input_per_mtok: 10
      output_per_mtok: 50
      cached_input_per_mtok: 1
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-6.1-sol
      input_per_mtok: 2
      output_per_mtok: 10
      cached_input_per_mtok: 0.10
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-6-luna
      input_per_mtok: 0.10
      output_per_mtok: 0.50
      cached_input_per_mtok: 0.01
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-6-sol
      input_per_mtok: 2
      output_per_mtok: 10
      cached_input_per_mtok: 0.20
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.6-sol
      input_per_mtok: 4
      output_per_mtok: 20
      cached_input_per_mtok: 0.40
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.6-terra
      input_per_mtok: 2
      output_per_mtok: 12
      cached_input_per_mtok: 0.20
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.6-luna
      input_per_mtok: 0.20
      output_per_mtok: 1.20
      cached_input_per_mtok: 0.02
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.5
      input_per_mtok: 5
      output_per_mtok: 30
      cached_input_per_mtok: 0.50
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.5-pro
      input_per_mtok: 30
      output_per_mtok: 180
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.4
      input_per_mtok: 2.50
      output_per_mtok: 15
      cached_input_per_mtok: 0.25
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.4-mini
      input_per_mtok: 0.75
      output_per_mtok: 4.50
      cached_input_per_mtok: 0.075
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.4-nano
      input_per_mtok: 0.20
      output_per_mtok: 1.25
      cached_input_per_mtok: 0.02
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.4-pro
      input_per_mtok: 30
      output_per_mtok: 180
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.2
      input_per_mtok: 1.75
      output_per_mtok: 14
      cached_input_per_mtok: 0.175
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.2-pro
      input_per_mtok: 21
      output_per_mtok: 168
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5.1
      input_per_mtok: 1.25
      output_per_mtok: 10
      cached_input_per_mtok: 0.125
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5
      input_per_mtok: 1.25
      output_per_mtok: 10
      cached_input_per_mtok: 0.125
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5-mini
      input_per_mtok: 0.25
      output_per_mtok: 2
      cached_input_per_mtok: 0.025
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5-nano
      input_per_mtok: 0.05
      output_per_mtok: 0.40
      cached_input_per_mtok: 0.005
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
    - model: gpt-5-pro
      input_per_mtok: 15
      output_per_mtok: 120
      currency: USD
      effective_date: "2026-10-13"
      source: https://developers.openai.com/api/docs/pricing
```

`model` должен совпадать со значением `model` агента (или глобальным `model`) в конфиге
точно, иначе попытка не найдёт тариф. Если ваш runtime передаёт модель с префиксом
провайдера (например, `anthropic/claude-opus-5-5`), скопируйте запись под это имя.
