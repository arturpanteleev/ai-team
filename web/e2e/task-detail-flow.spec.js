import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm } from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'

const webDir = path.dirname(fileURLToPath(import.meta.url)).replace(/\/e2e$/, '')
const repoDir = path.resolve(webDir, '..')

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', ...options })
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed:\n${result.stdout}\n${result.stderr}`)
  return result.stdout
}

function start(command, args, options = {}) {
  const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'], ...options })
  let output = ''
  child.stdout.on('data', (chunk) => { output += chunk })
  child.stderr.on('data', (chunk) => { output += chunk })
  child.outputText = () => output
  return child
}

async function unusedPort() {
  const server = net.createServer()
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address()
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  return port
}

async function stop(child) {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 5000)),
  ])
  if (child.exitCode === null) child.kill('SIGKILL')
}

async function waitForServer(url, child) {
  const deadline = Date.now() + 20_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`web server exited: ${child.outputText()}`)
    try {
      const response = await fetch(`${url}/api/auth/config`)
      if (response.ok) return
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  throw new Error(`web server did not start: ${child.outputText()}`)
}

const now = '2026-10-12T09:00:00Z'

function revision(id, content, revisionNumber) {
  return {
    run_id: 'run-manual', artifact_path: 'stages/idea/result.md', revision: revisionNumber,
    id, base_revision: revisionNumber > 1 ? 'idea-v1' : undefined, content,
    actor_id: 'owner', stage_id: 'idea', result: 'md', created_at: now,
    sha256: String(revisionNumber).repeat(64),
  }
}

const manualTemplate = {
  stages: [
    { id: 'idea', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human', skippable: true },
    { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
    { id: 'qa', title: 'Проверка', function: 'qa', result: 'md', executor: 'human', skippable: true },
    { id: 'delivery', title: 'План доставки', function: 'release_manager', result: 'approve', executor: 'human' },
  ],
  returns: [{ from: 'qa', to: 'idea' }],
}

const autoTemplate = {
  stages: [
    { id: 'build', title: 'Сборка', function: 'developer', result: 'md', executor: 'agent', agent: 'coder' },
    { id: 'verify', title: 'Проверка', function: 'qa', result: 'md', executor: 'agent', agent: 'tester' },
  ],
  returns: [],
}

const deliveryPlan = {
  schema_version: 1, branch: 'ai-team/run-manual', base_branch: 'main', remote: 'origin',
  remote_url: 'https://git.example/team/product.git', files: ['docs/idea.md', 'docs/analysis.md'],
  commit_message: 'docs: ручной маршрут', pr_title: 'Ручной маршрут', pr_body: 'Выкладываем после подтверждения.',
}

// Mirrors Server.taskAllowedActions (pkg/web/task_api.go). The same templates
// and phase cases are pinned by TestTaskAllowedActionsByPhaseMatchesBrowserContract
// in pkg/web/task_allowed_actions_test.go; change both together.
function allowedActions(template, stageID, phase, pending) {
  const actions = []
  if (!stageID) return actions
  const stage = template.stages.find((value) => value.id === stageID)
  if (phase === 'resumable' && stage?.agent) actions.push('run-agent')
  if (phase === 'waiting' && pending?.status === 'pending') {
    if (pending.kind === 'input' && pending.trigger === 'human_input') {
      actions.push('take', 'results')
      if (stage?.agent) actions.push('executor', 'run-agent')
    } else if (pending.kind === 'grill_questions') {
      actions.push('grill/answer', 'grill/new-version', 'grill/done')
    } else if (pending.trigger === 'delivery_plan') {
      actions.push('plan/approve')
    } else {
      if (pending.actions.includes('approve')) actions.push('confirm')
      if (pending.actions.includes('override_approve') || (pending.kind === 'check_decision' &&
        pending.trigger === 'stage_check' && pending.actions.includes('override') &&
        pending.targets?.override === stageID)) actions.push('override')
      if (stage?.agent && pending.actions.includes('retry')) actions.push('run-agent')
    }
  }
  if (stage?.skippable && (phase === 'waiting' || phase === 'resumable')) actions.push('skip')
  if (stage && phase === 'waiting' && template.returns.some((route) => route.from === stageID)) actions.push('return')
  return [...new Set(actions)]
}

function pendingApproval(runID, currentStage, pending) {
  const base = { run_id: runID, quorum: 'any', status: 'pending' }
  switch (pending) {
    case 'grill': return { ...base, id: 'grill-1', kind: 'grill_questions', attempt_id: 'analysis-attempt', from_stage: 'analysis', to_stage: 'analysis',
      trigger: 'grill_questions', subject_hash: 'd'.repeat(64), required_roles: ['product_owner'],
      actions: ['answer', 'new_version', 'done'], payload: { kind: 'grill_questions', round: 1, markdown: 'Какой сегмент проверяем первым?' } }
    case 'check': return { ...base, id: 'check-1', kind: 'approve', attempt_id: 'analysis-attempt', from_stage: 'analysis', to_stage: 'qa',
      trigger: 'stage_check', subject_hash: 'c'.repeat(64), required_roles: ['architect'], actions: ['approve', 'override_approve'] }
    case 'plan': return { ...base, id: 'plan-1', kind: 'approve', attempt_id: 'delivery-attempt', from_stage: 'delivery', to_stage: 'delivery',
      trigger: 'delivery_plan', subject_hash: 'e'.repeat(64), required_roles: ['release_manager'], actions: ['approve', 'reject'], payload: deliveryPlan }
    case 'input': return { ...base, id: `input-${currentStage}`, kind: 'input', attempt_id: '', from_stage: currentStage, to_stage: currentStage,
      trigger: 'human_input', subject_hash: 'a'.repeat(64), required_roles: ['product_owner'], actions: ['submit'] }
    default: return null
  }
}

function detail(mode, { stage: currentStage, phase, status, pending, owners }) {
  const runID = mode === 'auto' ? 'run-auto' : 'run-manual'
  const pinned = mode === 'auto' ? autoTemplate : manualTemplate
  const approval = pendingApproval(runID, currentStage, pending)

  const resolvedGrill = mode === 'manual' && ['analysis', 'qa', 'idea', 'delivery'].includes(currentStage) ? [{
    ...detailApproval(runID), status: 'resolved', resolved_action: 'answer_questions', resolved_at: now,
    decisions: [{ actor_id: 'owner', actor_role: 'product_owner', action: 'answer_questions', subject_hash: 'd'.repeat(64), comment: 'Сегмент малого бизнеса', decided_at: now }],
  }] : []
  const run = { id: mode === 'auto' ? 2 : 1, run_id: runID, feature: mode === 'auto' ? 'Автоматический маршрут' : 'Ручной маршрут',
    status, started_at: now, ...(status === 'completed' ? { completed_at: now } : {}) }
  return {
    run, task: 'Проверить маршрут целиком из браузера', mode,
    external_links: mode === 'manual' ? ['https://tracker.example/TASK-62'] : [],
    template: { valid: true, template: `${mode}-template`, title: mode === 'auto' ? 'Шаблон авто' : 'Шаблон ручной',
      version: 'f'.repeat(64), stages: pinned.stages, returns: pinned.returns },
    state: { run_id: runID, task: 'Проверить маршрут целиком из браузера', phase,
      next_stage: currentStage, pending_approval_id: approval?.id ?? '', attempt_ordinal: 2 },
    stages: mode === 'manual' && ['analysis', 'qa', 'idea', 'delivery'].includes(currentStage) ? [{
      id: 19, pipeline_run_id: 1, attempt_id: 'analysis-attempt', stage_index: 2, agent_name: 'analysis', status: 'passed',
      started_at: now, completed_at: now, duration_ms: 9000,
      checks_json: JSON.stringify([{ name: 'required-sections', class: 'content', command: ['ai-team', 'check'], policy: 'required', status: 'passed', exit_code: 0 }]),
    }] : [],
    approvals: [...(approval ? [approval] : []), ...resolvedGrill], stage_owners: owners,
    results: mode === 'manual' ? { idea: [revision('idea-v1', 'Первая версия идеи', 1), revision('idea-v2', 'Вторая версия идеи', 2)] } : {},
    allowed_actions: allowedActions(pinned, currentStage, phase, approval), delivery: { status: 'not_requested' },
  }
}

function detailApproval(runID) {
  return {
    id: 'grill-0', run_id: runID, attempt_id: 'analysis-attempt', from_stage: 'analysis', to_stage: 'analysis',
    trigger: 'graph_outcome:blocked', subject_hash: 'd'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
    actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Какой сегмент проверяем первым?' },
  }
}

function installController(page) {
  // Server lifecycle phases: a human stage waits on a human_input approval, an
  // agent stage that is not running yet is resumable (run status "stopped").
  const waitingFor = (stage, pending) => ({ stage, phase: 'waiting', status: 'waiting_for_approval', pending })
  const pausedAt = (stage) => ({ stage, phase: 'resumable', status: 'stopped', pending: null })
  const finished = { stage: '', phase: 'terminal', status: 'completed', pending: null }
  const manual = { ...waitingFor('idea', 'input'), owners: {} }
  const auto = { ...pausedAt('build'), owners: {} }
  const actionsSeen = []
  page.route('**/api/runs/**', async (route) => {
    const request = route.request()
    const pathname = new URL(request.url()).pathname
    if (pathname.endsWith('/events')) {
      const runID = pathname.split('/')[3]
      await route.fulfill({ json: { events: [{ version: 1, cursor: 1, run_id: runID, sequence: 1,
        type: 'run_started', timestamp: now, data: { mode: runID === 'run-auto' ? 'auto' : 'manual' } },
      { version: 1, cursor: 2, run_id: runID, sequence: 2, type: 'transition_selected', stage: 'analysis',
        timestamp: now, data: { outcome: 'continue' } }], next_cursor: 2 } })
      return
    }
    if (pathname.includes('/logs/')) {
      await route.fulfill({ json: { run_id: 'run-manual', attempt_id: 'analysis-attempt', offset: 0, truncated: false, content: 'Проверено: required-sections' } })
      return
    }
    const runID = pathname.split('/')[3]
    const state = runID === 'run-auto' ? auto : manual
    if (request.method() === 'GET' && pathname === `/api/runs/${runID}`) {
      await route.fulfill({ json: detail(runID === 'run-auto' ? 'auto' : 'manual', state) })
      return
    }
    if (request.method() === 'POST') {
      const action = pathname.split('/').at(-1)
      actionsSeen.push(pathname)
      if (runID === 'run-auto') {
        if (action === 'run-agent' && state.stage === 'build') Object.assign(state, pausedAt('verify'))
        else if (action === 'run-agent' && state.stage === 'verify') Object.assign(state, finished)
      } else if (action === 'take') {
        state.owners = { [state.stage]: { stage_id: state.stage, approval_id: `input-${state.stage}`, actor_id: 'local-user', actor_role: 'product_owner', taken_at: now } }
      } else if (action === 'results' && state.stage === 'idea') {
        Object.assign(state, pausedAt('analysis'), { owners: {} })
      } else if (action === 'run-agent') {
        Object.assign(state, waitingFor('analysis', 'grill'))
      } else if (pathname.endsWith('/grill/answer')) {
        Object.assign(state, waitingFor('analysis', 'check'))
      } else if (action === 'override') {
        Object.assign(state, waitingFor('qa', 'input'))
      } else if (action === 'return') {
        Object.assign(state, waitingFor('idea', 'input'))
      } else if (action === 'skip') {
        Object.assign(state, waitingFor('delivery', 'plan'))
      } else if (pathname.endsWith('/plan/approve')) {
        Object.assign(state, finished)
      }
      await route.fulfill({ json: { ok: true } })
      return
    }
    await route.continue()
  })
  page.route('**/api/pipelines/*/artifacts', (route) => route.fulfill({ json: [] }))
  return actionsSeen
}

test('task detail traverses the manual and automatic templates using allowed actions', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-task-detail-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  let webServer

  try {
    run('go', ['build', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    await mkdir(target, { recursive: true })
    run(binary, ['init', '--target', target], { cwd: repoDir })
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist'), '--scheduler-db', '.ai-team/scheduler.db'], { cwd: target })
    await waitForServer(baseURL, webServer)

    await page.goto(`${baseURL}/tasks`)
    const localToken = (await readFile(path.join(target, '.ai-team', 'web.token'), 'utf8')).trim()
    await page.getByLabel('Токен доступа').fill(localToken)
    await page.getByRole('button', { name: 'Войти' }).click()
    await expect(page.getByRole('heading', { name: 'Задачи', exact: true })).toBeVisible()

    const actionsSeen = installController(page)
    await page.goto(`${baseURL}/tasks/run-manual`)
    await expect(page.getByRole('heading', { name: 'Бизнес-идея', exact: true })).toBeVisible()
    await expect(page.getByRole('link', { name: 'https://tracker.example/TASK-62' })).toBeVisible()
    await page.getByText('Сравнить версии 1 и 2').click()
    await expect(page.getByText('Первая версия идеи')).toHaveCount(2)
    await expect(page.getByText('Вторая версия идеи')).toHaveCount(2)
    await expect(page.getByRole('heading', { name: 'Бизнес-идея', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Завершить грилл без ответа' })).toHaveCount(0)

    // Human input, then agent work and its question/check reviews.
    await page.getByRole('button', { name: 'Взять этап' }).click()
    await expect(page.getByText('Ответственный: local-user')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toBeVisible()
    await page.getByRole('button', { name: 'Сдать результат' }).click()
    await page.getByLabel('Результат в Markdown').fill('Идея подтверждена')
    await page.getByRole('button', { name: 'Отправить результат' }).click()
    await expect(page.getByRole('heading', { name: 'Анализ', level: 2 })).toBeVisible()
    await expect(page.getByRole('button', { name: /запустить агента/i })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toHaveCount(0)

    await page.getByRole('button', { name: /запустить агента/i }).click()
    await expect(page.getByRole('heading', { name: 'Грилл: вопросы и ответы' })).toBeVisible()
    await expect(page.getByRole('region', { name: 'Грилл: вопросы и ответы' }).getByText('Какой сегмент проверяем первым?')).toBeVisible()
    await page.getByLabel('Ваш ответ').fill('Малый бизнес')
    await page.getByRole('button', { name: 'Ответить и продолжить' }).click()
    await expect(page.getByRole('button', { name: 'Подтвердить этап' })).toBeVisible()
    await page.getByText('Другие действия: пропуск, обход проверки, возврат').click()
    await expect(page.getByRole('button', { name: 'Обойти проверку' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toHaveCount(0)
    await expect(page.getByText('История грилла')).toBeVisible()
    await expect(page.getByText('Сегмент малого бизнеса')).toBeVisible()
    await page.getByText('Журнал задачи: таймлайн, этапы, логи и лента событий').click()
    await expect(page.getByRole('link', { name: 'Открыть лог этапа' })).toBeVisible()
    await page.getByRole('button', { name: 'Этап analysis: показать детали' }).click()
    await expect(page.getByText('required-sections', { exact: true })).toBeVisible()

    await page.getByLabel('Причина обхода проверки').fill('Проверено вручную')
    await page.getByRole('button', { name: 'Обойти проверку' }).click()
    await expect(page.getByRole('heading', { name: 'Проверка', level: 2 })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Вернуть этап' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Пропустить этап' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Подтвердить этап' })).toHaveCount(0)
    await page.getByLabel('Причина возврата').fill('Перепроверить предпосылку')
    await page.getByRole('button', { name: 'Вернуть этап' }).click()
    await expect(page.getByRole('heading', { name: 'Идея', level: 2 })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Пропустить этап' })).toBeVisible()
    await page.getByLabel('Причина пропуска').fill('Результат уже проверен')
    await page.getByRole('button', { name: 'Пропустить этап' }).click()
    await expect(page.getByRole('heading', { name: 'План доставки', level: 2 })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Подтвердить план' })).toBeVisible()
    const planContent = page.getByRole('region', { name: 'Содержимое плана доставки' })
    await expect(planContent.getByText('ai-team/run-manual', { exact: true })).toBeVisible()
    await expect(planContent.getByText('docs/analysis.md', { exact: true })).toBeVisible()
    await expect(planContent.getByText('Выкладываем после подтверждения.', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Подтвердить план' }).click()
    await expect(page.getByText('завершена')).toBeVisible()

    // Automatic tasks expose only the two agent actions and finish without human controls.
    await page.goto(`${baseURL}/tasks/run-auto`)
    await expect(page.getByRole('heading', { name: 'Бизнес-идея', exact: true })).toBeVisible()
    await page.getByRole('region', { name: 'Бизнес-идея' }).getByText('Подробности').click()
    await expect(page.getByRole('region', { name: 'Бизнес-идея' }).getByText('Авто', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: /запустить агента/i })).toBeVisible()
    for (const invalidAction of ['Взять этап', 'Сдать результат', 'Подтвердить этап', 'Обойти проверку', 'Пропустить этап', 'Вернуть этап', 'Подтвердить план']) {
      await expect(page.getByRole('button', { name: invalidAction })).toHaveCount(0)
    }
    await page.getByRole('button', { name: /запустить агента/i }).click()
    await expect(page.getByRole('heading', { name: 'Проверка', level: 2 })).toBeVisible()
    await page.getByRole('button', { name: /запустить агента/i }).click()
    await expect(page.getByText('завершена')).toBeVisible()

    expect(actionsSeen.map((value) => value.replace(/\/api\/runs\/run-(?:manual|auto)/, '/api/runs/<run>'))).toEqual([
      '/api/runs/<run>/stages/idea/take',
      '/api/runs/<run>/stages/idea/results',
      '/api/runs/<run>/stages/analysis/run-agent',
      '/api/runs/<run>/stages/analysis/grill/answer',
      '/api/runs/<run>/stages/analysis/override',
      '/api/runs/<run>/stages/qa/return',
      '/api/runs/<run>/stages/idea/skip',
      '/api/runs/<run>/plan/approve',
      '/api/runs/<run>/stages/build/run-agent',
      '/api/runs/<run>/stages/verify/run-agent',
    ])
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
