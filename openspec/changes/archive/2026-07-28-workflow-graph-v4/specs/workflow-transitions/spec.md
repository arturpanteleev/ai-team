## MODIFIED Requirements

### Requirement: Переход определяется доменным состоянием

Pipeline MUST вычислять execution, decision и outcome до выбора
декларативного edge, approval или terminal target.

#### Scenario: Негативный вердикт

- **КОГДА** попытка имеет execution=succeeded и негативный verdict
- **ТОГДА** её outcome MUST быть rejected
- **И** pipeline MUST выбрать `rejected` edge compiled graph
