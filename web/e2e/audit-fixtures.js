// Fixtures of the UI audit (S16): one scenario per task state of S11 and the S06/S14/S15 extras.
// A scenario is the full answer of the task API for one moment of a task, plus who is looking
// at it. The browser specs install them with `installApi` and assert what the page shows;
// the same scenarios produce the screenshots in docs/ui-audit/.

const at = '2026-10-12T09:00:00Z'
const later = '2026-10-12T09:20:00Z'
const hash = (char) => char.repeat(64)

export const template = {
  stages: [
    { id: 'intent', title: 'Бизнес-идея', function: 'product_owner', result: 'approve', executor: 'human' },
    { id: 'product_spec', title: 'Продуктовая спецификация', function: 'product_owner', result: 'md', executor: 'human', agent: 'analyst' },
    { id: 'implementation', title: 'Техническая реализация', function: 'developer', result: 'link', link_kind: 'pr', executor: 'agent', agent: 'coder' },
    { id: 'code_review', title: 'Код-ревью', function: 'reviewer', result: 'md', executor: 'human', agent: 'reviewer', skippable: true },
    { id: 'qa', title: 'Тестирование', function: 'qa', result: 'md', executor: 'human', agent: 'tester', skippable: true },
    { id: 'deploy', title: 'Выкладка', function: 'release_manager', result: 'link', link_kind: 'build', executor: 'human' },
  ],
  returns: [{ from: 'code_review', to: 'implementation' }, { from: 'qa', to: 'implementation' }],
}

const human = (actor) => ({ kind: 'human', ...(actor ? { actor } : {}), source: actor ? 'attempt' : 'configured' })
const agent = (name = 'coder') => ({ kind: 'internal_agent', agent: name, name: 'agentcli', source: 'configured' })
const external = { kind: 'external_agent', name: 'container-review', agent: 'reviewer', source: 'configured' }

function entry(id, index, over = {}) {
  return {
    stage_id: id, index, status: 'not_reached', readiness: 'not_reached', phase: 'none', outcome: 'none', executor: human(),
    current: false, visited: false, visit_count: 0, active_visit: 0, allowed_actions: [], visits: [], ...over,
  }
}

function visit(number, over = {}) {
  return { number, active: false, entered_at: at, entered_via: 'transition', outcome: 'passed', executor: human('alice'), attempts: [], ...over }
}

const passedEntry = (id, index, executor = human('alice')) => entry(id, index, {
  status: 'passed', readiness: 'left', outcome: 'passed', executor, visited: true, visit_count: 1,
  visits: [visit(1, { executor, left_at: later })],
})

// stages before `currentIndex` passed, the current one as given, the rest not reached.
function statuses(currentIndex, current, overrides = {}) {
  const items = template.stages.map((stage, index) => {
    if (index < currentIndex) return overrides[stage.id] ?? passedEntry(stage.id, index + 1, stage.executor === 'agent' ? agent(stage.agent) : human('alice'))
    if (index === currentIndex) return { ...entry(stage.id, index + 1, { current: true, visited: true, visit_count: 1, active_visit: 1,
      visits: [visit(1, { active: true, outcome: 'pending' })] }), ...current }
    return overrides[stage.id] ?? entry(stage.id, index + 1)
  })
  return { schema_version: 1, terminal: false, current_stage: template.stages[currentIndex]?.id, stages: items }
}

const inputApproval = (stage, roles = ['product_owner']) => ({
  id: `input-${stage}`, run_id: 'run-audit', attempt_id: '', from_stage: stage, to_stage: stage, kind: 'input', trigger: 'human_input',
  subject_hash: hash('a'), required_roles: roles, quorum: 'any', actions: ['submit', 'run_agent'], status: 'pending', created_at: at,
})

// The confirmation of a transition as the controller writes it: trigger graph_outcome:<outcome> (a
// failed check is a check_decision with the trigger stage_check, never this), from_stage is the stage
// being confirmed, to_stage the stage the task moves to.
const transitionApproval = (stage, to, roles = ['product_owner'], extra = {}) => ({
  id: `gate-${stage}`, run_id: 'run-audit', attempt_id: `${stage}-attempt-1`, from_stage: stage, to_stage: to, kind: 'approve', trigger: 'graph_outcome:passed',
  subject_hash: hash('c'), required_roles: roles, quorum: 'any', actions: ['approve', 'reject'], targets: { approve: to }, status: 'pending', created_at: later, ...extra,
})

// A failed required check: kind check_decision, trigger stage_check, the decision stays at the stage.
const checkApproval = (stage, roles = ['product_owner'], extra = {}) => ({
  id: `check-${stage}`, run_id: 'run-audit', attempt_id: `${stage}-attempt-1`, from_stage: stage, to_stage: stage, kind: 'check_decision', trigger: 'stage_check',
  subject_hash: hash('c'), required_roles: roles, quorum: 'any', actions: ['new_version', 'override', 'return'],
  targets: { new_version: stage, override: stage, return: stage }, status: 'pending', created_at: later, payload: { kind: 'check_decision', status: 'fail' }, ...extra,
})

const attempt = (stage, index, status, extra = {}) => ({
  id: 100 + index, pipeline_run_id: 7, attempt_id: `${stage}-attempt-${index}`, stage_index: index, agent_name: stage, status,
  started_at: at, ...(status === 'running' ? {} : { completed_at: later, duration_ms: 1_200_000 }), ...extra,
})

const gate = (name, execution, verdict, extra = {}) => ({
  gate_name: name, gate: name, gate_version: 1, mode: 'required', group: 0, attempts: 1,
  blocking: execution !== 'succeeded' || verdict === 'fail', warning: false, retryable: false, overridden: false,
  attempt: { attempt_id: `gate-${name}-1`, gate: name, gate_name: name, mode: 'required', group: 0, execution, verdict, reason: `причина ${name}`, started_at: at, finished_at: later },
  ...extra,
})

function wave(stage, gates, transition, extra = {}) {
  return { stage_id: stage, visit: 1, stage_attempt_id: `${stage}-attempt-1`, artifact_sha256: hash('a'), current: true, attempts: [], gates,
    transition: { allowed: false, state: 'waiting', reasons: [], blocking: [], warnings: [], retryable: [], overridden: [], ...transition }, ...extra }
}

const gatesOf = (stage, waveValue, over = {}) => ({
  run_id: 'run-audit', can_override: false, overrides: waveValue.override ? [waveValue.override] : [], warnings: [],
  stages: waveValue ? [{ stage_id: stage, title: template.stages.find((item) => item.id === stage)?.title, groups: [waveValue.gates.map((item) => item.gate_name)], waves: [waveValue], current: waveValue }] : [],
  ...over,
})

const noTracker = { configured: false, sync_status: 'not_configured' }
const trackerLink = { task_id: 7, tracker: 'fake', issue_key: 'DEMO-1', linked_by: 'owner', linked_at: at }
const inboundRequest = (over = {}) => ({
  tracker: 'fake', change_id: 'chg-1', task_id: 7, issue_key: 'DEMO-1', from_status: 'In Progress', to_status: 'Done', origin: 'external', author: 'jira-user',
  changed_at: later, state: 'conflict', action: 'advance', reason: 'этап «Код-ревью» ждёт решения человека: карточка не может его подменить',
  suggested_action: 'принять результат этапа', detected_at: later, updated_at: later, allowed_actions: ['confirm'], ...over,
})
const delivery = (over = {}) => ({ event_id: 5, task_id: 7, tracker: 'fake', issue_key: 'DEMO-1', event_type: 'attempt_started', stage: 'implementation', status: 'failed',
  attempts: 3, last_error: 'tracker: 503 Service Unavailable', created_at: at, updated_at: later, ...over })

const results = { product_spec: [{ run_id: 'run-audit', artifact_path: 'stages/product_spec/result.md', revision: 1, id: 'spec-v1', content: '# Спецификация\n\n## Критерии приёмки\n- Приветствие по имени.', actor_id: 'alice', stage_id: 'product_spec', result: 'md', created_at: at, sha256: hash('1') }] }

// roles: who looks at the page. `admin` is the system right, not a process function.
export const scenarios = {
  'ready-human': {
    title: 'Готов к работе: человек',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'input-product_spec' },
    approvals: [inputApproval('product_spec')],
    allowed: ['take', 'results', 'executor', 'run-agent'],
    statuses: statuses(1, { status: 'ready', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human(), blocked_by: { kind: 'human_input' }, allowed_actions: ['take', 'results', 'executor', 'run-agent'] }),
    expect: { who: 'Человек', buttons: ['Сдать результат', 'Взять этап', 'Поручить агенту'], notButtons: ['Сменить исполнителя: на человека'], chip: 'Готов к работе' },
  },
  'agent-override': {
    title: 'Этап отдан агенту человеком: можно вернуть человеку',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'input-product_spec', executor_overrides: { product_spec: { executor: 'agent', previous_executor: 'human' } } },
    approvals: [inputApproval('product_spec')],
    allowed: ['take', 'results', 'executor', 'run-agent'],
    statuses: statuses(1, { status: 'ready', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: { ...agent('analyst'), source: 'override' }, blocked_by: { kind: 'human_input' }, allowed_actions: ['take', 'results', 'executor', 'run-agent'] }),
    expect: { who: 'выбран человеком', buttons: ['Сдать результат', 'Поручить агенту', 'Сменить исполнителя: на человека'], chip: 'Готов к работе' },
  },
  'working-human': {
    title: 'В работе: человек',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'input-product_spec' },
    approvals: [inputApproval('product_spec')],
    owners: { product_spec: { stage_id: 'product_spec', approval_id: 'input-product_spec', actor_id: 'alice', actor_role: 'product_owner', taken_at: later } },
    allowed: ['results', 'executor', 'run-agent'],
    statuses: statuses(1, { status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: human('alice'), allowed_actions: ['results', 'executor', 'run-agent'] }),
    expect: { who: 'ответственный: alice', buttons: ['Сдать результат'], chip: 'В работе' },
  },
  'working-agent': {
    title: 'В работе: внутренний агент',
    roles: ['product_owner'],
    run: { status: 'running' },
    state: { phase: 'running', next_stage: 'implementation' },
    stages: [attempt('implementation', 1, 'running')],
    allowed: [],
    statuses: statuses(2, { status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: agent(), allowed_actions: [] }),
    expect: { who: 'Внутренний агент: coder', chip: 'В работе', text: 'Агент работает' },
  },
  'working-external': {
    title: 'В работе: внешний агент',
    roles: ['product_owner'],
    run: { status: 'running' },
    state: { phase: 'running', next_stage: 'code_review' },
    stages: [attempt('code_review', 1, 'running')],
    allowed: [],
    statuses: statuses(3, { status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: external, allowed_actions: [] }),
    expect: { who: 'Внешний агент: container-review', chip: 'В работе' },
  },
  checking: {
    title: 'Проверяется: гейты работают',
    roles: ['product_owner'],
    run: { status: 'running' },
    state: { phase: 'running', next_stage: 'product_spec' },
    stages: [attempt('product_spec', 1, 'passed')],
    allowed: [],
    statuses: statuses(1, { status: 'checking', readiness: 'engaged', phase: 'checking', outcome: 'pending', executor: human('alice'), blocked_by: { kind: 'gate', gates: ['required_sections', 'agent_review'] }, allowed_actions: [] }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('required_sections', 'succeeded', 'pass'), gate('agent_review', 'running', undefined, { blocking: true })],
      { state: 'waiting', reasons: ['agent_review ещё выполняется'] })),
    expect: { chip: 'Проверяется', text: 'agent_review' },
  },
  'awaiting-human': {
    title: 'Ждёт человека: принять результат',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    // While the confirmation waits the checkpoint already points at the next stage; the stage that waits is product_spec.
    state: { phase: 'waiting', next_stage: 'implementation', pending_approval_id: 'gate-product_spec' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [transitionApproval('product_spec', 'implementation')],
    results,
    allowed: ['confirm'],
    statuses: statuses(1, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'passed', executor: human('alice'), blocked_by: { kind: 'approval', roles: ['product_owner'], actions: ['approve'] }, allowed_actions: ['confirm'], action_stage_id: 'implementation' }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('required_sections', 'succeeded', 'pass'), gate('agent_review', 'succeeded', 'pass')], { allowed: true, state: 'passed' })),
    expect: { chip: 'Ждёт человека', buttons: ['Принять и перейти: Техническая реализация'], decides: { stage: 'product_spec', approvalID: 'gate-product_spec', action: 'approve' } },
  },
  grill: {
    title: 'Ждёт человека: вопросы грилла',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'grill-1' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [{ ...checkApproval('product_spec'), id: 'grill-1', kind: 'grill_questions', trigger: 'grill_questions', actions: ['answer', 'new_version', 'done'], targets: {},
      payload: { kind: 'grill_questions', round: 1, markdown: '## Вопросы проверки\n\n1. Какой сегмент пользователей проверяем первым?\n2. Что считаем успехом через месяц?' } }],
    allowed: ['grill/answer', 'grill/new-version', 'grill/done'],
    statuses: statuses(1, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human('alice'), blocked_by: { kind: 'grill_questions', roles: ['product_owner'] }, allowed_actions: ['grill/answer', 'grill/new-version', 'grill/done'] }),
    expect: { chip: 'Ждёт человека', buttons: ['Ответить и продолжить', 'Отправить новую версию', 'Завершить грилл без ответа'], text: 'Какой сегмент пользователей' },
  },
  passed: {
    title: 'Пройден: задача завершена',
    roles: ['product_owner'],
    run: { status: 'completed', completed_at: later },
    state: { phase: 'terminal', next_stage: '' },
    allowed: [],
    delivery: { status: 'recorded', record: { schema_version: 1, run_id: 'run-audit', feature: 'greet-by-name', plan_hash: hash('e'), commit_sha: '3f2a9c1d', pr_url: 'https://github.com/example/greeter/pull/12', performed_at: later } },
    statuses: { schema_version: 1, terminal: true, stages: template.stages.map((stage, index) => passedEntry(stage.id, index + 1)) },
    expect: { text: 'Задача завершена', noActions: true },
  },
  'blocked-gate': {
    title: 'Заблокирован: обязательный гейт провален',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'check-failed' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [{ ...checkApproval('product_spec'), id: 'check-failed' }],
    allowed: ['override'],
    statuses: statuses(1, { status: 'blocked', readiness: 'engaged', phase: 'awaiting_human', outcome: 'blocked', executor: human('alice'), blocked_by: { kind: 'check_decision', roles: ['product_owner'] }, allowed_actions: ['override'] }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('required_sections', 'succeeded', 'fail')], { state: 'blocked', reasons: ['нет раздела «Критерии приёмки»'], blocking: ['required_sections'] })),
    expect: { chip: 'Заблокирован', text: 'Переход заблокирован', notButtons: ['Обойти проверку'], hint: 'может только администратор' },
  },
  rejected: {
    title: 'Отклонён: результат возвращён',
    roles: ['reviewer'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'code_review', pending_approval_id: 'input-code_review' },
    approvals: [inputApproval('code_review', ['reviewer'])],
    allowed: ['results', 'run-agent', 'skip', 'return'],
    statuses: statuses(3, { status: 'rejected', readiness: 'engaged', phase: 'awaiting_human', outcome: 'rejected', executor: human('bob'), allowed_actions: ['results', 'run-agent', 'skip', 'return'] }),
    expect: { chip: 'Отклонён', buttons: ['Пропустить этап', 'Вернуть на доработку: Техническая реализация', 'Сдать результат'] },
  },
  'returned-second-visit': {
    title: 'Возврат: второе посещение этапа',
    roles: ['developer'],
    run: { status: 'running' },
    state: { phase: 'running', next_stage: 'implementation' },
    stages: [attempt('implementation', 1, 'passed'), attempt('implementation', 2, 'running')],
    allowed: [],
    statuses: statuses(2, {
      status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: agent(), visit_count: 2, active_visit: 2, allowed_actions: [],
      visits: [visit(1, { executor: agent(), left_to: 'code_review', outcome: 'passed' }),
        visit(2, { active: true, outcome: 'pending', executor: agent(), entered_from: 'code_review', entered_via: 'return' })],
    }),
    expect: { chip: 'В работе', text: 'Посещение 2 из 2 (возврат)' },
  },
  'skipped-stage': {
    title: 'Пропущен этап',
    roles: ['release_manager'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'deploy', pending_approval_id: 'input-deploy' },
    approvals: [inputApproval('deploy', ['release_manager'])],
    allowed: ['take', 'results'],
    statuses: statuses(5, { status: 'ready', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human(), blocked_by: { kind: 'human_input' }, allowed_actions: ['take', 'results'] }, {
      qa: entry('qa', 5, { status: 'skipped', readiness: 'left', outcome: 'skipped', visited: true, visit_count: 1, visits: [visit(1, { outcome: 'skipped', skipped: true })] }),
    }),
    expect: { chip: 'Пропущен', buttons: ['Сдать результат'] },
  },
  interrupted: {
    title: 'Прервана: можно продолжить',
    roles: ['developer'],
    run: { status: 'interrupted' },
    state: { phase: 'resumable', next_stage: 'implementation' },
    allowed: [],
    statuses: statuses(2, { status: 'ready', readiness: 'engaged', phase: 'none', outcome: 'pending', executor: agent(), blocked_by: { kind: 'run_state', reason: 'сервер был перезапущен во время выполнения' }, allowed_actions: [] }),
    expect: { chip: 'Готов к работе', buttons: ['Продолжить задачу'], notButtons: ['Отменить задачу'] },
  },
  override: {
    title: 'Обход гейта администратором (красный)',
    roles: ['product_owner', 'admin'],
    canOverride: true,
    run: { status: 'running' },
    state: { phase: 'running', next_stage: 'implementation' },
    stages: [attempt('product_spec', 1, 'passed')],
    allowed: [],
    statuses: statuses(2, { status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: agent(), allowed_actions: [] }, {
      product_spec: { ...passedEntry('product_spec', 2), overridden: true },
    }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('required_sections', 'succeeded', 'fail', { overridden: true })], { allowed: true, state: 'overridden', overridden: ['required_sections'] }, {
      override: { stage: 'product_spec', scope: 'product_spec-attempt-1', artifact_sha256: hash('a'), reason: 'срочный релиз, критерии согласованы устно', actor_id: 'root', authority: 'system_admin', at: later,
        gates: [{ gate_name: 'required_sections', attempt_id: 'gate-required_sections-1', execution: 'succeeded', verdict: 'fail' }] } }), { can_override: true }),
    expect: { flag: 'override', text: 'Обход провала' },
  },
  advisory: {
    title: 'Рекомендательный гейт не выполнен (янтарный)',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'implementation', pending_approval_id: 'gate-product_spec' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [transitionApproval('product_spec', 'implementation')],
    results,
    allowed: ['confirm'],
    statuses: statuses(1, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'passed', executor: human('alice'), warnings: 1, blocked_by: { kind: 'approval', roles: ['product_owner'], actions: ['approve'] }, allowed_actions: ['confirm'], action_stage_id: 'implementation' }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('required_sections', 'succeeded', 'pass'),
      gate('style_review', 'succeeded', 'fail', { mode: 'advisory', blocking: false, warning: true })], { allowed: true, state: 'passed_with_warnings', warnings: ['style_review'] })),
    expect: { flag: 'advisory', text: 'Рекомендация не выполнена', buttons: ['Принять и перейти: Техническая реализация'] },
  },
  'gate-error': {
    title: 'Ошибка технического гейта: повтор на той же версии',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'check-failed' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [{ ...checkApproval('product_spec'), id: 'check-failed', actions: ['new_version', 'return', 'retry_gate'], targets: { new_version: 'product_spec', return: 'product_spec', retry_gate: 'product_spec' }, payload: { kind: 'check_decision', status: 'error' } }],
    allowed: ['gate-retry'],
    statuses: statuses(1, { status: 'blocked', readiness: 'engaged', phase: 'awaiting_human', outcome: 'blocked', executor: human('alice'), blocked_by: { kind: 'check_decision', roles: ['product_owner'] }, allowed_actions: ['gate-retry'] }),
    gates: gatesOf('product_spec', wave('product_spec', [gate('agent_review', 'error', undefined, { retryable: true })], { state: 'error', reasons: ['agent_review: runtime недоступен'], retryable: ['agent_review'] })),
    expect: { chip: 'Заблокирован', buttons: ['Повторить гейт: agent_review'], text: 'Ошибка запуска' },
  },
  'tracker-conflict': {
    title: 'Трекер: конфликт и ожидающий входящий запрос',
    roles: ['architect'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'code_review', pending_approval_id: 'input-code_review' },
    approvals: [inputApproval('code_review', ['reviewer'])],
    allowed: [],
    statuses: statuses(3, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human(), blocked_by: { kind: 'human_input' }, allowed_actions: [] }),
    tracker: { configured: true, tracker: 'fake', sync_status: 'inbound_pending', link: trackerLink, conflicts: [], deliveries: [delivery(), delivery({ event_id: 4, status: 'delivered', attempts: 1, last_error: undefined, transition_to: 'In Progress', event_type: 'attempt_finished' })], inbound: [inboundRequest()] },
    tab: 'Трекер',
    expect: { text: 'Статус карточки «In Progress» → «Done»', buttons: ['Снять связь', 'Повторить отправку в трекер (1)', 'Синхронизировать сейчас'], mainButtons: ['Закрыть запрос без действий'] },
  },
  'tracker-unlinked': {
    title: 'Трекер: задача не связана с карточкой',
    roles: ['product_owner'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'input-product_spec' },
    approvals: [inputApproval('product_spec')],
    allowed: ['take', 'results', 'executor', 'run-agent'],
    statuses: statuses(1, { status: 'ready', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human(), blocked_by: { kind: 'human_input' }, allowed_actions: ['take', 'results'] }),
    tracker: { configured: true, tracker: 'fake', sync_status: 'not_linked', link: null, conflicts: [], deliveries: [], inbound: [] },
    expect: { buttons: ['Связать с карточкой', 'Синхронизировать сейчас'], text: 'Не связана с карточкой' },
  },
  'developer-on-owner-stage': {
    title: 'Нет прав: разработчик на этапе владельца продукта',
    roles: ['developer'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'product_spec', pending_approval_id: 'input-product_spec' },
    approvals: [inputApproval('product_spec')],
    // The server lists what the state offers; the function of the stage is product_owner, so this developer may do none of it.
    allowed: ['take', 'results', 'executor', 'run-agent'],
    viewer: [],
    statuses: statuses(1, { status: 'ready', readiness: 'engaged', phase: 'awaiting_human', outcome: 'pending', executor: human(), blocked_by: { kind: 'human_input' }, allowed_actions: ['take', 'results', 'executor', 'run-agent'] }),
    expect: { chip: 'Готов к работе', noButtons: ['Сдать результат', 'Взять этап', 'Поручить агенту', 'Пропустить этап'], hint: 'Нужна роль: Владелец продукта' },
  },
  // The confirmation of code_review (reviewer) while the task moves to qa: return and skip must address code_review.
  'transition-return': {
    title: 'Подтверждение перехода: возврат относится к подтверждаемому этапу',
    roles: ['reviewer'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'qa', pending_approval_id: 'gate-code_review' },
    stages: [attempt('code_review', 1, 'passed')],
    approvals: [transitionApproval('code_review', 'qa', ['reviewer'], { actions: ['approve', 'reject', 'return_to_implementation'], targets: { approve: 'qa', return_to_implementation: 'implementation' } })],
    allowed: ['confirm', 'return'],
    statuses: statuses(3, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'passed', executor: human('bob'), blocked_by: { kind: 'approval', roles: ['reviewer'], actions: ['approve'] }, allowed_actions: ['confirm', 'return'], action_stage_id: 'qa' }),
    expect: { chip: 'Ждёт человека', buttons: ['Принять и перейти: Тестирование', 'Вернуть на доработку: Техническая реализация'], noButtons: ['Пропустить этап'], decides: { stage: 'code_review', approvalID: 'gate-code_review', action: 'approve' }, returns: { stage: 'code_review', to: 'implementation' } },
  },
  'no-rights': {
    title: 'Нет прав: разработчик смотрит на решение владельца',
    roles: ['developer'],
    run: { status: 'waiting_for_approval' },
    state: { phase: 'waiting', next_stage: 'implementation', pending_approval_id: 'gate-product_spec' },
    stages: [attempt('product_spec', 1, 'passed')],
    approvals: [transitionApproval('product_spec', 'implementation')],
    results,
    // The server offers the confirmation to anyone (allowed_actions); viewer_actions is what this principal may do: nothing.
    allowed: ['confirm'],
    viewer: [],
    statuses: statuses(1, { status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'passed', executor: human('alice'), blocked_by: { kind: 'approval', roles: ['product_owner'], actions: ['approve'] }, allowed_actions: ['confirm'], action_stage_id: 'implementation' }),
    tracker: { configured: true, tracker: 'fake', sync_status: 'ok', link: trackerLink, conflicts: [], deliveries: [], inbound: [] },
    expect: { chip: 'Ждёт человека', noButtons: ['Принять и перейти'], hint: 'Нужна роль: Владелец продукта. Без неё недоступно: Принять и перейти', trackerHint: 'Связь с карточкой меняют: Владелец продукта, Архитектор' },
  },
}

export function detail(id, options = {}) {
  const scenario = scenarios[id]
  const run = { id: 7, run_id: 'run-audit', feature: 'greet-by-name', status: 'running', started_at: at, ...scenario.run }
  return {
    run, task: 'Добавить приветствие по имени в CLI.\n\nНужно, чтобы greeter принимал флаг --name и здоровался.', mode: 'manual', external_links: ['https://tracker.example/DEMO-1'],
    template: { valid: true, template: 'idea-to-prod', title: 'От идеи до продакшена', version: 'f'.repeat(64), ...template },
    state: { run_id: 'run-audit', task: 'Добавить приветствие по имени', phase: 'running', next_stage: '', attempt_ordinal: 1, ...scenario.state },
    stages: scenario.stages ?? [], approvals: scenario.approvals ?? [], stage_owners: scenario.owners ?? {}, results: scenario.results ?? {},
    allowed_actions: scenario.allowed ?? [], viewer_actions: scenario.viewer ?? scenario.allowed ?? [], delivery: scenario.delivery ?? { status: 'not_requested' }, stage_statuses: scenario.statuses,
    ...(options.extra ?? {}),
  }
}

function events(count) {
  return Array.from({ length: count }, (_, index) => ({
    version: 1, cursor: index + 1, run_id: 'run-audit', sequence: index + 1, type: index % 7 === 0 ? 'attempt_started' : 'transition_selected',
    stage: 'product_spec', timestamp: at, data: { line: `строка лога ${index} ${'очень длинное сообщение '.repeat(12)}` },
  }))
}

// Stubs the API of the task page for one scenario. Everything else answers 404 so a stray call is visible.
export async function installApi(page, id, options = {}) {
  const scenario = scenarios[id]
  const eventCount = options.events ?? 3
  const artifacts = options.artifacts ?? 0
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const { pathname } = new URL(request.url())
    const json = (body, status = 200) => route.fulfill({ status, json: body })
    if (pathname === '/api/session') return json({ csrf_token: 'csrf-audit', principal: { actor_id: 'audit-user', roles: scenario.roles } })
    if (pathname === '/api/auth/config') return json({ authentication_required: true, team_management_enabled: true })
    if (pathname === '/api/auth/me') return json({ authentication_required: true, principal: { actor_id: 'audit-user', roles: scenario.roles } })
    if (pathname === '/api/version') return json({ version: 'audit', commit: 'audit' })
    if (pathname === '/api/tracker') return json(scenario.tracker ? { configured: true, tracker: 'fake', failed_deliveries: 1, link_conflicts: 0, inbound: { configured: true, poll_interval_seconds: 60, status: 'ok', delay_seconds: 20 } } : { configured: false })
    if (pathname === '/api/runs/run-audit') return json(detail(id))
    if (pathname === '/api/runs/run-audit/tracker') return json(scenario.tracker ?? noTracker)
    if (pathname === '/api/runs/run-audit/gates') return json(scenario.gates ?? { run_id: 'run-audit', can_override: scenario.canOverride === true, stages: [], overrides: [], warnings: [] })
    if (pathname === '/api/runs/run-audit/events') return json({ events: events(eventCount), next_cursor: eventCount })
    if (pathname === '/api/pipelines/7/artifacts') return json(Array.from({ length: artifacts }, (_, index) => ({
      name: `file-${index}.md`, path: `attempts/implementation-attempt-1/artifacts/file-${index}.md`, run_id: 'run-audit', size: 100, mod_time: at })))
    if (pathname.startsWith('/api/pipelines/7')) return json({ run: detail(id).run, stages: [] })
    if (pathname.includes('/logs/')) return json({ run_id: 'run-audit', attempt_id: 'a', offset: 0, truncated: false, content: 'лог' })
    if (pathname.startsWith('/api/runs/run-audit/metrics') || pathname.startsWith('/api/metrics')) return json({ by_stage: [] })
    return json({ error: `audit fixture: ${pathname} is not stubbed` }, 404)
  })
}
