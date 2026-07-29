## 1. Graph schema и compiler

- [x] 1.1 Добавить schema v4 workflow, edges, approval policies и max_visits
- [x] 1.2 Реализовать строгую валидацию ссылок, outcomes, достижимости и terminal path
- [x] 1.3 Обнаруживать циклы и требовать положительный max_visits
- [x] 1.4 Компилировать schemas 1–3 в совместимый legacy graph
- [x] 1.5 Перевести default config на schema v4

## 2. Engine

- [x] 2.1 Выбирать следующий узел по outcome edge
- [x] 2.2 Применять edge approval roles/quorum/actions без TTY-зависимости
- [x] 2.3 Сохранять edge target в lifecycle/evidence и восстанавливать visits
- [x] 2.4 Инвалидировать downstream attempts при обратном ребре
- [x] 2.5 Останавливать цикл до AI call при превышении max_visits

## 3. Web и evidence

- [x] 3.1 Сохранять compiled graph в immutable workflow snapshot
- [x] 3.2 Добавить confined read API workflow конкретного run
- [x] 3.3 Показать nodes, edges, approvals и текущую позицию на detail page

## 4. Завершение

- [x] 4.1 Добавить unit/integration/E2E coverage graph route и resume
- [x] 4.2 Обновить русскую документацию
- [x] 4.3 Выполнить strict validation и `make verify`
- [x] 4.4 Синхронизировать спецификации и архивировать change
