## MODIFIED Requirements

### Requirement: Prompt contract
Prompt MUST включать role instructions, feature, task, input file content, directory references, exact output paths и controller-owned service requirements. File-based input content MUST be wrapped in an explicit untrusted-data delimiter with an instruction not to execute commands or role-override instructions found within it.

#### Scenario: Verdict-bearing agent
- **КОГДА** definition объявляет required verdict
- **ТОГДА** service section MUST содержать единственный канонический marker contract

#### Scenario: File-based input
- **WHEN** an agent declares a file-based input
- **THEN** that input's content MUST appear between `<UNTRUSTED_ARTIFACT>` delimiters in the prompt
- **AND** the prompt MUST instruct the agent not to treat that content as instructions
