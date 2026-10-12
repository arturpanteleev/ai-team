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

function detail(mode, currentStage, allowed, status = 'waiting_for_approval', pending = null) {
  const runID = mode === 'auto' ? 'run-auto' : 'run-manual'
  const stageDefs = mode === 'auto' ? [
    { id: 'build', title: 'Сборка', function: 'developer', result: 'md', executor: 'agent', agent: 'coder' },
    { id: 'verify', title: 'Проверка', function: 'qa', result: 'md', executor: 'agent', agent: 'tester' },
  ] : [
    { id: 'idea', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human', skippable: true },
    { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
    { id: 'qa', title: 'Проверка', function: 'qa', result: 'md', executor: 'human', skippable: true },
    { id: 'delivery', title: 'План доставки', function: 'release_manager', result: 'approve', executor: 'human' },
  ]
  const approval = pending === 'grill' ? {
    id: 'grill-1', run_id: runID, attempt_id: 'analysis-attempt', from_stage: 'analysis', to_stage: 'analysis',
    trigger: 'graph_outcome:blocked', subject_hash: 'd'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
    actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Какой сегмент проверяем первым?' }, status: 'pending',
  } : pending === 'check' ? {
    id: 'check-1', run_id: runID, attempt_id: 'analysis-attempt', from_stage: 'analysis', to_stage: 'qa',
    trigger: 'stage_check', subject_hash: 'c'.repeat(64), required_roles: ['architect'], quorum: 'any',
    actions: ['approve', 'override_approve'], status: 'pending',
  } : pending === 'plan' ? {
    id: 'plan-1', run_id: runID, attempt_id: 'delivery-attempt', from_stage: 'delivery', to_stage: 'delivery',
    trigger: 'delivery_plan', subject_hash: 'e'.repeat(64), required_roles: ['release_manager'], quorum: 'any',
    actions: ['approve'], payload: { kind: 'delivery_plan', markdown: 'Выкладываем после подтверждения.' }, status: 'pending',
  } : pending === 'input' ? {
    id: 'input-1', run_id: runID, attempt_id: '', from_stage: currentStage, to_stage: currentStage,
    trigger: 'human_input', subject_hash: 'a'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
    actions: ['submit'], status: 'pending',
  } : null

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
      version: 'f'.repeat(64), stages: stageDefs, returns: mode === 'manual' ? [{ from: 'qa', to: 'idea' }] : [] },
    state: { run_id: runID, task: 'Проверить маршрут целиком из браузера', phase: status === 'completed' ? 'terminal' : 'waiting',
      next_stage: status === 'completed' ? '' : currentStage, pending_approval_id: approval?.id ?? '', attempt_ordinal: 2 },
    stages: mode === 'manual' && ['analysis', 'qa', 'idea', 'delivery'].includes(currentStage) ? [{
      id: 19, pipeline_run_id: 1, attempt_id: 'analysis-attempt', stage_index: 2, agent_name: 'analysis', status: 'passed',
      started_at: now, completed_at: now, duration_ms: 9000,
      checks_json: JSON.stringify([{ name: 'required-sections', class: 'content', command: ['ai-team', 'check'], policy: 'required', status: 'passed', exit_code: 0 }]),
    }] : [],
    approvals: [...(approval ? [approval] : []), ...resolvedGrill], stage_owners: {},
    results: mode === 'manual' ? { idea: [revision('idea-v1', 'Первая версия идеи', 1), revision('idea-v2', 'Вторая версия идеи', 2)] } : {},
    allowed_actions: allowed, delivery: { status: 'not_requested' },
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
  const manual = { stage: 'idea', actions: ['take', 'results'], status: 'waiting_for_approval', pending: 'input' }
  const auto = { stage: 'build', actions: ['run-agent'], status: 'running', pending: null }
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
      await route.fulfill({ json: detail(runID === 'run-auto' ? 'auto' : 'manual', state.stage, state.actions, state.status, state.pending) })
      return
    }
    if (request.method() === 'POST') {
      const action = pathname.split('/').at(-1)
      actionsSeen.push(pathname)
      if (runID === 'run-auto') {
        if (action === 'run-agent' && state.stage === 'build') Object.assign(state, { stage: 'verify', actions: ['run-agent'], pending: null })
        else if (action === 'run-agent' && state.stage === 'verify') Object.assign(state, { stage: '', actions: [], status: 'completed', pending: null })
      } else if (action === 'take') {
        state.actions = ['results']
      } else if (action === 'results' && state.stage === 'idea') {
        Object.assign(state, { stage: 'analysis', actions: ['run-agent'], status: 'running', pending: null })
      } else if (action === 'run-agent') {
        Object.assign(state, { stage: 'analysis', actions: ['grill/answer', 'grill/done'], status: 'waiting_for_approval', pending: 'grill' })
      } else if (action === 'answer' || pathname.includes('/grill/answer')) {
        Object.assign(state, { stage: 'analysis', actions: ['confirm', 'override'], pending: 'check' })
      } else if (action === 'override') {
        Object.assign(state, { stage: 'qa', actions: ['take', 'results', 'skip', 'return'], pending: 'input' })
      } else if (action === 'return') {
        Object.assign(state, { stage: 'idea', actions: ['skip'], pending: null })
      } else if (action === 'skip') {
        Object.assign(state, { stage: 'delivery', actions: ['plan/approve'], pending: 'plan' })
      } else if (pathname.endsWith('/plan/approve')) {
        Object.assign(state, { stage: '', actions: [], status: 'completed', pending: null })
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
    await expect(page.getByRole('heading', { name: 'Задачи' })).toBeVisible()

    const actionsSeen = installController(page)
    await page.goto(`${baseURL}/tasks/run-manual`)
    await expect(page.getByRole('heading', { name: 'Ручной маршрут' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'https://tracker.example/TASK-62' })).toBeVisible()
    await page.getByText('Сравнить версии 1 и 2').click()
    await expect(page.getByText('Первая версия идеи')).toHaveCount(2)
    await expect(page.getByText('Вторая версия идеи')).toHaveCount(2)
    await expect(page.getByText('Задача создана')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Завершить грилл без ответа' })).toHaveCount(0)

    // Human input, then agent work and its question/check reviews.
    await page.getByRole('button', { name: 'Взять этап' }).click()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Взять этап' })).toHaveCount(0)
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
    await expect(page.getByRole('button', { name: 'Обойти проверку' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toHaveCount(0)
    await expect(page.getByText('История грилла')).toBeVisible()
    await expect(page.getByText('Сегмент малого бизнеса')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Открыть лог этапа' })).toBeVisible()
    await page.getByRole('button', { name: 'Этап analysis: показать детали' }).click()
    await expect(page.getByText('required-sections')).toBeVisible()

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
    await expect(page.getByRole('button', { name: 'Сдать результат' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Подтвердить план' }).click()
    await expect(page.getByText('завершена')).toBeVisible()

    // Automatic tasks expose only the two agent actions and finish without human controls.
    await page.goto(`${baseURL}/tasks/run-auto`)
    await expect(page.getByRole('heading', { name: 'Автоматический маршрут' })).toBeVisible()
    await expect(page.getByText('Режим: Авто')).toBeVisible()
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
