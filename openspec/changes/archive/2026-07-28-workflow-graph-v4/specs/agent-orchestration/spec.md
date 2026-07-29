## MODIFIED Requirements

### Requirement: Порядок выполнения пайплайна

Schema v4 MUST выполнять первый узел из `workflow.entry`, а каждый следующий
узел — только по выбранному outcome edge и resolved human approval. Legacy
schemas MUST сохранять строгий порядок массива pipeline.

#### Scenario: Последовательное выполнение schema v4

- **КОГДА** graph run запускается
- **ТОГДА** entry node MUST выполняться первым
- **И** следующий node MUST совпадать с exact target выбранного edge action
- **И** дефолтный успешный маршрут MUST быть analyst → architect → coder →
  reviewer → tester → verifier → deployer

#### Scenario: Сбой без recovery edge

- **КОГДА** attempt возвращает outcome без соответствующего edge
- **ТОГДА** pipeline MUST остановиться
- **И** последующие узлы MUST NOT выполняться
- **И** MUST быть сгенерирован итоговый отчёт с указанием упавшего этапа

### Requirement: Настройка пайплайна

Schema v4 pipeline MUST определять nodes, а `workflow` MUST определять entry,
outcome edges и approvals; legacy pipeline list MUST компилироваться в
последовательный graph.

#### Scenario: Кастомный graph

- **КОГДА** пользователь задаёт entry и edges между выбранными pipeline nodes
- **ТОГДА** система MUST выполнять только маршрут, выбранный recorded outcomes
  и human decisions
