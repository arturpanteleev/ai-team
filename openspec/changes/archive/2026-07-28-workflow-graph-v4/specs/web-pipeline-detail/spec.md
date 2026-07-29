## ADDED Requirements

### Requirement: Визуализация маршрута

Run detail MUST показывать compiled graph, approval policy рёбер и текущую
позицию lifecycle.

#### Scenario: Graph run открыт

- **КОГДА** пользователь открывает detail graph run
- **ТОГДА** UI MUST показать entry, nodes, outcome edges, targets, roles,
  quorum и max_visits
