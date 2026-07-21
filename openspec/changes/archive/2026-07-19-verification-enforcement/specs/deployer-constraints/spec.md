## ADDED Requirements

### Requirement: Работа в отдельной ветке
Deployer MUST NOT пушить в default-ветку репозитория.

#### Scenario: Создание ветки перед коммитом
- **КОГДА** deployer начинает работу и текущая ветка — default (master/main)
- **ТОГДА** он MUST создать и переключиться на ветку `ai-team/{feature}` до `git add`

#### Scenario: Уже на фиче-ветке
- **КОГДА** текущая ветка не default
- **ТОГДА** deployer MUST использовать её без создания новой

#### Scenario: Push только фиче-ветки
- **КОГДА** deployer выполняет `git push`
- **ТОГДА** push MUST идти в ветку, отличную от default, с установкой upstream при необходимости
