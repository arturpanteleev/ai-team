## Контекст

`web/package.json` допускал `react-router-dom ^7.18.1`, а lockfile фиксировал
7.18.1. После первоначальной проверки npm опубликовал advisories и для
7.11.0: текущий audit считает уязвимыми одновременно 6.0.0–7.17.0 и
7.12.0–8.2.0, а latest остаётся 7.18.1. Поддерживаемой версии без
high-severity findings на текущую дату нет.

Приложение использует только три routes, browser history, ссылки,
back-navigation, один path parameter, wildcard artifact path и query parameter
`view`. Полная framework routing dependency для этого набора не обязательна.

## Цели / Не-цели

**Цели:**

- восстановить зелёный `npm audit --audit-level=high`;
- сохранить fail-closed dependency gate;
- сохранить существующие URL, навигацию, тесты и production build;
- сделать локальную frontend verification последовательной с CI;
- зафиксировать воспроизводимый dependency graph.

**Не-цели:**

- переход на другую routing-библиотеку;
- обновление остальных frontend-зависимостей;
- подавление advisory из-за отсутствия RSC в приложении;
- изменение UI, HTTP API или runtime workflow.

## Решения

### D1. Заменить React Router локальным browser-history adapter

В `web/src/router.tsx` реализуется узкий adapter: `BrowserRouter`,
`MemoryRouter`, `Link`, `NavLink`, `useNavigate`, `useParams`,
`useSearchParams` и `useLocation`. Он использует стандартные History и URL API
браузера и поддерживает только фактически нужную приложению семантику.

Альтернатива с downgrade на 7.11.0 отклонена после повторного audit: версия
тоже имеет high-severity advisories. Переход на другую внешнюю routing-
библиотеку отклонён как новая supply-chain зависимость для трёх routes.

### D2. Не добавлять audit exceptions

Gate остаётся `npm audit --audit-level=high`. Ни npm overrides, ни локальный
allow-list advisory не используются. Если dependency graph снова станет
уязвимым, локальная и CI verification обязаны упасть.

### D3. Проверить compatibility существующим набором frontend checks

После удаления зависимости выполняются lint, Vitest, TypeScript/Vite build и
проверка, что build не изменил предварительно сохранённый snapshot `web/dist`.
Сравнение с Git HEAD локально не используется: во время разработки корректно
пересобранный dist сам является намеренным uncommitted diff. Отдельная миграция
routing API не планируется, если существующий код проходит эти проверки.

### D4. Локальная последовательность повторяет frontend CI

Frontend-часть `make verify` сохраняет временный snapshot `web/dist`, выполняет
`npm ci`, build, сравнивает результат со snapshot, затем запускает lint, tests
и high-severity audit. Это не меняет состав gate, а устраняет различие порядка
и пропущенную локально проверку актуальности `web/dist`.

## Риски / Компромиссы

- **Риск:** downgrade может изменить редко используемое поведение router.
  **Снижение:** frontend tests, typecheck/build и ручная проверка существующих
  route declarations.
- **Риск:** будущий advisory затронет и 7.11.0.
  **Снижение:** audit остаётся fail-closed и обнаружит это без изменения кода.
- **Компромисс:** используется не latest-версия routing-библиотеки.
  Это осознанно до появления исправленной версии вне уязвимого диапазона.

## План миграции

1. Добавить локальный routing adapter и переключить существующие imports.
2. Удалить `react-router-dom` из dependency graph и обновить lockfile.
3. Выполнить frontend lint/tests/build/audit.
4. Пересобрать и проверить `web/dist`.
5. Выполнить полный `make verify`.

Rollback — возврат dependency declarations, lockfile и `web/dist` одним
изменением; отдельной миграции данных нет.

## Открытые вопросы

Нет.
