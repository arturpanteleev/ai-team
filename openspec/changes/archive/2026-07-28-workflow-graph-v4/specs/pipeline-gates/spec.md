## ADDED Requirements

### Requirement: Edge approval gate

Для schema v4 controller MUST создавать approval из policy выбранного edge,
а не из позиции узла в массиве.

#### Scenario: Approval action меняет target

- **КОГДА** resolved action указывает target, отличный от edge `to`
- **ТОГДА** lifecycle next stage MUST стать action target
- **И** решение и выбранное ребро MUST попасть в immutable evidence
