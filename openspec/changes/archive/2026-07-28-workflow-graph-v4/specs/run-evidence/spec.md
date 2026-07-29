## ADDED Requirements

### Requirement: Evidence compiled graph

Immutable workflow snapshot MUST содержать фактически исполненный compiled
graph, включая entry, edges, approval policies и max_visits.

#### Scenario: Resume graph run

- **КОГДА** graph run возобновляется после restart
- **ТОГДА** engine MUST проверить snapshot digest
- **И** восстановить visit counters и next node из immutable evidence/lifecycle
