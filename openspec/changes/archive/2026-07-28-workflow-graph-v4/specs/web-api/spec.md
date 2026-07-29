## ADDED Requirements

### Requirement: Immutable workflow API

Web API MUST отдавать compiled workflow snapshot exact существующего run без
доступа к произвольным run files.

#### Scenario: Workflow запрошен

- **КОГДА** клиент запрашивает workflow существующего run
- **ТОГДА** API MUST вернуть immutable `workflow.json`
- **И** MUST NOT подменять его текущим config
