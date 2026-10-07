# Controller-owned state for an isolated cloud pilot

## Proposal

**ID**: MAJ-07 architecture gate  |  **Priority**: Major

### ЧТО

Ввести границу controller/worker, позволяющую запускать вычисление в отдельном
ограниченном executor без writable доступа к human approvals, lifecycle,
authoritative evidence, control-plane databases или admin API. До этой границы
проект не заявляет поддерживаемый isolated cloud pilot.

### ПОЧЕМУ

Сейчас `ai-team worker` исполняет полный pipeline. Web/controller и worker
используют один SQLite approval store, так что worker может напрямую подделать
или изменить approvals, несмотря на то что легитимные human decisions проходят
через authenticated controller. Lifecycle и evidence всё ещё используют
filesystem stores в persistent workspace. Ни общий read-write volume, ни общая
controller DB, ни контейнеризация/Compose сами по себе не создают границу
изоляции.

### Границы

- Controller владеет admission, очередью, lifecycle, human decisions,
  authoritative evidence, delivery credentials и side effects.
- Worker получает ограниченный job input, изолированный writable candidate
  workspace и секреты только для разрешённого runtime.
- Job-scoped transport передаёт ограниченный результат обратно controller-у;
  controller валидирует его и сам фиксирует authoritative state.
- Нет capability для создания/изменения решения человека или вызова
  административного control API.
- Конкретный облачный runtime и deployment manifest выбираются после
  согласования протокола и проверок; этот delta не обозначает инфраструктуру
  как уже поддерживаемую.

### Критерий отказа

Если исполняемый workload может менять существующий approval/evidence либо
обратиться к admin API, containment считается неэффективным, даже если процесс
работает в отдельном контейнере. См. [статус cloud pilot](../../../docs/guides/cloud-pilot.md)
для текущего threat model и воспроизводимых release checks.
