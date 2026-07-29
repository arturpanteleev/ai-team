# Проектирование

## Доменная модель

`PendingApproval` содержит immutable subject перехода: approval_id, run_id,
attempt_id, from/to stage, trigger, subject hash, required roles, quorum,
allowed actions и action→target mapping. `ApprovalDecision` содержит actor_id,
actor_role, action, comment, subject hash и decided_at.

Approval относится к конкретному transition proposal, а не к названию stage.
Повторная попытка или изменение subject создаёт новый approval id.

## Хранение и атомарность

Approval documents находятся в
`.ai-team/state/approvals/<run_id>/<approval_id>.json`. Controller создаёт и
обновляет их атомарным rename. Решение принимается только при точном совпадении
subject hash, допустимой роли/action и non-terminal status. Повтор actor/role
идемпотентен только для идентичного решения.

Quorum `any` завершается первым допустимым решением. Quorum `all` требует
одинакового action от каждой required role; конфликтующие actions отклоняются.

## Pipeline pause/resume

После AI-stage pipeline строит subject hash, создаёт approval, записывает
`approval_requested`, сохраняет lifecycle phase `waiting` с approval id и
выходит с управляемым exit code 3. Это не terminal run.

CLI decision transport записывает решение независимо от worker process.
`run --resume` загружает resolved approval, перепроверяет evidence subject и
продолжает с target, связанного с action. Approval не может быть применён к
другому run или изменённому candidate/artifact.

## TTY и compatibility transport

Интерактивный prompt создаёт тот же persisted approval и записывает typed
decision от `local-user`; отдельной TTY-бизнес-логики нет.
`--approve-gates` остаётся локальным compatibility transport: в момент
достижения перехода он записывает exact-subject decision от явного local
actor, а не отключает approval model.

## Review loopback

Негативный reviewer verdict всегда создаёт approval, если существует
допустимый loopback target. `return_to_coder` увеличивает retry counter,
передаёт review outputs coder-у и инвалидирует downstream attempts.
`override_approve` продолжает вперёд, `reject` завершает run, а
`request_information` оставляет его waiting с новым уточняющим решением.

## Граница change

HTTP endpoints и browser UI входят в следующий change. Здесь реализуются
домен, persistence, CLI transport и pipeline semantics, которыми web будет
пользоваться без дублирования правил.
