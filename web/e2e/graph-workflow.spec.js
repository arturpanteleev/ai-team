// GRA-03/04/05 + WF-04: the built bundle of the graph and workflow pages.
// The API is stubbed; only web/dist and the router are under test.
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'

const webDir = path.dirname(fileURLToPath(import.meta.url)).replace(/\/e2e$/, '')
const dist = path.join(webDir, 'dist')
const types = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json' }

const workflows = {
  active_workflow_id: 'idea-to-prod',
  workflows: [
    { id: 'idea-to-prod', title: 'SDLC', description: 'Standard flow', version: 3, graph_count: 1, active: true },
    { id: 'content-factory', title: 'Content', version: 2, graph_count: 1, active: false },
  ],
}

const graph = {
  schema_version: 7, entry: 'spec', mode: 'class', graph_id: 'sdlc', workflow_id: 'idea-to-prod', title: 'SDLC graph',
  nodes: [{ id: 'spec', max_visits: 3 }, { id: 'build' }],
  edges: [{ from: 'spec', outcome: 'passed', to: 'build' }, { from: 'build', outcome: 'passed', to: '$complete' }],
  returns: [{ from: 'build', to: 'spec', max_visits: 3 }],
}

// Снимок реальной конфигурации дефолтного шаблона:
// config.DefaultProfile(config.ProfileStandard).CompiledGraph() + cfg.Returns.
// tech_design: прямые passed/skipped, обратный rejected, возврат return_to_product_spec;
// code_review/qa/observation возвращают return_to_implementation.
const defaultGraph = {
  schema_version: 7, entry: 'intent', mode: 'class', graph_id: 'sdlc-default', workflow_id: 'idea-to-prod',
  title: 'SDLC default template',
  nodes: [{ id: 'intent' }, { id: 'product_spec', max_visits: 3 }, { id: 'tech_design' }, { id: 'implementation', max_visits: 3 },
    { id: 'code_review' }, { id: 'qa' }, { id: 'deploy' }, { id: 'observation' }, { id: 'acceptance' }],
  edges: [
    { from: 'intent', outcome: 'passed', to: 'product_spec' },
    { from: 'product_spec', outcome: 'passed', to: 'tech_design' },
    { from: 'tech_design', outcome: 'skipped', to: 'implementation' },
    { from: 'tech_design', outcome: 'passed', to: 'implementation' },
    { from: 'implementation', outcome: 'passed', to: 'code_review' },
    { from: 'code_review', outcome: 'passed', to: 'qa' },
    { from: 'qa', outcome: 'passed', to: 'deploy' },
    { from: 'deploy', outcome: 'passed', to: 'observation' },
    { from: 'observation', outcome: 'passed', to: 'acceptance' },
    { from: 'acceptance', outcome: 'passed', to: '$complete' },
    { from: 'acceptance', outcome: 'rejected', to: 'product_spec' },
    { from: 'code_review', outcome: 'rejected', to: 'implementation' },
    { from: 'observation', outcome: 'rejected', to: 'implementation' },
    { from: 'qa', outcome: 'rejected', to: 'implementation' },
    { from: 'tech_design', outcome: 'rejected', to: 'product_spec' },
  ],
  returns: [
    { from: 'tech_design', to: 'product_spec' },
    { from: 'code_review', to: 'implementation' },
    { from: 'qa', to: 'implementation' },
    { from: 'qa', to: 'product_spec' },
    { from: 'observation', to: 'implementation' },
    { from: 'acceptance', to: 'product_spec' },
  ],
}

const runGraph = {
  ...graph, mode: 'run', run_id: 'run-1', run_status: 'running', current_stage: 'build',
  stage_statuses: {
    schema_version: 7, run_status: 'running', terminal: false, current_stage: 'build',
    stages: [
      { stage_id: 'spec', title: 'Спецификация', index: 0, status: 'passed', readiness: 'left', phase: 'none', outcome: 'passed', executor: { kind: 'human', source: 'attempt' }, current: false, visited: true, visit_count: 1, active_visit: 1, visits: [], allowed_actions: [] },
      { stage_id: 'build', title: 'Сборка', index: 1, status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending', executor: { kind: 'human', source: 'configured' }, current: true, visited: true, visit_count: 2, active_visit: 2, visits: [], allowed_actions: [] },
    ],
  },
}

const workflowDetail = {
  id: 'idea-to-prod', title: 'SDLC', description: 'Standard flow', version: 3, active: true,
  graphs: [graph], tasks: [{ run_id: 'run-1', feature: 'Фича', status: 'running', current_stage: 'build' }],
  entry_gate_groups: [[{ gate: 'verdict_marker', version: 1 }]],
  exit_gate_groups: [[{ gate: 'human_approval', version: 1 }]],
}

const taskDetail = {
  run: { id: 5, run_id: 'run-1', feature: 'Фича', status: 'running', started_at: '2026-10-12T09:00:00Z' },
  task: 'Фича', mode: 'manual',
  template: {
    valid: true, template: 'sdlc', title: 'SDLC', version: 'a'.repeat(64),
    stages: [{ id: 'spec', title: 'Спецификация', function: 'po', result: 'md', executor: 'human' },
      { id: 'build', title: 'Сборка', function: 'developer', result: 'md', executor: 'human' }],
    returns: [{ from: 'build', to: 'spec', max_visits: 3 }],
  },
  state: { run_id: 'run-1', task: 'Фича', phase: 'running', next_stage: 'build', attempt_ordinal: 1 },
  stages: [], approvals: [], stage_owners: {}, results: {}, allowed_actions: [],
  delivery: { status: 'not_requested' },
}

let server
let baseURL

test.beforeAll(async () => {
  server = createServer(async (request, response) => {
    const pathname = decodeURIComponent(new URL(request.url, 'http://x').pathname)
    let file = path.join(dist, pathname)
    try {
      if (!(await stat(file)).isFile()) throw new Error('not a file')
    } catch {
      file = path.join(dist, 'index.html')
    }
    response.setHeader('content-type', types[path.extname(file)] ?? 'application/octet-stream')
    response.end(await readFile(file))
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  baseURL = `http://127.0.0.1:${server.address().port}`
})

test.afterAll(async () => {
  await new Promise((resolve) => server.close(resolve))
})

async function installApi(page) {
  await page.route('**/api/**', async (route) => {
    const { pathname } = new URL(route.request().url())
    const json = (body, status = 200) => route.fulfill({ status, json: body })
    if (pathname === '/api/session') return json({ csrf_token: 'csrf', principal: { actor_id: 'owner@example.com', roles: ['product_owner'] } })
    if (pathname === '/api/auth/config') return json({ authentication_required: true, team_management_enabled: false, sso_enabled: false })
    if (pathname === '/api/auth/me') return json({ authentication_required: true, principal: { actor_id: 'owner@example.com', roles: ['product_owner'] } })
    if (pathname === '/api/version') return json({ version: 'graph-e2e', commit: 'graph-e2e' })
    if (pathname === '/api/tracker') return json({ configured: false })
    if (pathname === '/api/workflows') return json(workflows)
    if (pathname === '/api/workflows/idea-to-prod') return json(workflowDetail)
    if (pathname === '/api/graphs/sdlc') return json(graph)
    if (pathname === '/api/graphs/sdlc-default') return json(defaultGraph)
    if (pathname === '/api/runs/run-1/graph') return json(runGraph)
    if (pathname === '/api/runs/run-1') return json(taskDetail)
    if (pathname === '/api/runs/run-1/events') return json({ events: [], next_cursor: 0 })
    if (pathname === '/api/runs/run-1/gates') return json({ run_id: 'run-1', can_override: false, stages: [], overrides: [], warnings: [] })
    if (pathname === '/api/runs/run-1/tracker') return json({ configured: false, sync_status: 'not_configured' })
    if (pathname === '/api/pipelines/5/artifacts') return json([])
    if (pathname.startsWith('/api/metrics')) return json({ by_stage: [] })
    return json({ error: `graph e2e: ${pathname} is not stubbed` }, 404)
  })
}

async function open(page, target, { theme = 'light', width = 1440, height = 900, reducedMotion = 'no-preference' } = {}) {
  await page.addInitScript((value) => { try { localStorage.setItem('ai-team:theme', value) } catch {} }, theme)
  await page.emulateMedia({ reducedMotion })
  await page.setViewportSize({ width, height })
  await installApi(page)
  await page.goto(`${baseURL}${target}`)
}

function overflow(page) {
  return page.evaluate(() => ({
    page: document.documentElement.scrollWidth - window.innerWidth,
    controls: [...document.querySelectorAll('main a, main button')].filter((element) => {
      const bounds = element.getBoundingClientRect()
      return bounds.width > 0 && (bounds.left < -1 || bounds.right > window.innerWidth + 1)
    }).map((element) => `${element.tagName}.${element.className}`),
  }))
}

test('workflow list leads to a card and its class graph', async ({ page }) => {
  await open(page, '/workflows')
  await expect(page.getByRole('heading', { name: 'Workflow', level: 1 })).toBeVisible()
  await page.getByRole('link', { name: /SDLC/ }).click()
  await expect(page).toHaveURL(/\/workflows\/idea-to-prod$/)
  await expect(page.getByRole('region', { name: 'Гейты входа (эффективные)' })).toContainText('verdict_marker@1')

  await page.getByRole('link', { name: /SDLC graph/ }).click()
  await expect(page).toHaveURL(/\/graphs\/sdlc$/)
  await expect(page.getByRole('group', { name: 'Схема графа' })).toBeVisible()
  await expect(page.getByText('return_to_spec · макс. 3')).toBeVisible()
  await expect(page.getByText('Возврат (return_to_*)')).toBeVisible()
})

test('the run graph highlights the current stage and its outgoing edges', async ({ page }) => {
  await open(page, '/tasks/run-1')
  await page.getByRole('link', { name: 'Граф прогона' }).click()
  await expect(page).toHaveURL(/\/runs\/run-1\/graph$/)
  await expect(page.getByRole('link', { name: 'Открыть задачу: Сборка' })).toHaveAttribute('aria-current', 'step')
  expect(await page.locator('svg [data-active="true"]').count()).toBeGreaterThan(0)
})

test('the default template draws returns apart from backward outcomes and keeps labels apart', async ({ page }) => {
  await open(page, '/graphs/sdlc-default')
  const diagram = page.getByRole('group', { name: 'Схема графа' })
  await expect(diagram.getByText('return_to_product_spec', { exact: true }).first()).toBeVisible()
  await expect(diagram.getByText('return_to_implementation', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('Обратный переход (rejected)')).toBeVisible()
  await expect(page.getByText('Возврат (return_to_*)')).toBeVisible()

  const line = (kind) => diagram.locator(`g[data-kind="${kind}"] path`).first()
  const lineStyle = (kind) => line(kind).evaluate((element) => {
    const style = getComputedStyle(element)
    return { stroke: style.stroke, dash: style.strokeDasharray }
  })
  const backward = await lineStyle('back')
  const returned = await lineStyle('return')
  expect(backward.dash).toBe('none')
  expect(returned.dash).toMatch(/^6px,?\s+4px$/)
  expect(returned.stroke).toBe(backward.stroke)

  // tech_design: passed + skipped идут прямыми рёбрами, rejected и return_to_product_spec — дугами.
  const kinds = await diagram.locator('g[data-kind]').evaluateAll((groups) => groups.map((group) => group.getAttribute('data-kind')))
  expect(kinds.filter((kind) => kind === 'forward')).toHaveLength(10)
  expect(kinds.filter((kind) => kind === 'back')).toHaveLength(5)
  expect(kinds.filter((kind) => kind === 'return')).toHaveLength(6)

  const boxes = await diagram.locator('g[data-kind] rect').evaluateAll((elements) => elements.map((element) => {
    const box = element.getBoundingClientRect()
    return { left: box.left, top: box.top, right: box.right, bottom: box.bottom }
  }))
  expect(boxes.length).toBeGreaterThanOrEqual(21)
  const collisions = []
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      const a = boxes[i]
      const b = boxes[j]
      if (a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5) {
        collisions.push([i, j])
      }
    }
  }
  expect(collisions).toEqual([])
})

test('the passed legend swatch matches the passed node colour', async ({ page }) => {
  await open(page, '/runs/run-1/graph')
  const node = page.locator('svg g[class*="nodePassed"] rect').first()
  await expect(node).toBeVisible()
  const swatch = page.locator('ul[aria-label="Обозначения"] li').filter({ hasText: /^\s*Пройден\s*$/ }).locator('span').first()
  const colours = (locator) => locator.evaluate((element) => {
    const style = getComputedStyle(element)
    return { background: style.backgroundColor, border: style.borderTopColor, fill: style.fill, stroke: style.stroke }
  })
  const passed = await colours(node)
  const legend = await colours(swatch)
  expect(legend.background).toBe(passed.fill)
  expect(legend.border).toBe(passed.stroke)
})

test('both graph pages fit 390px, keep the focus order and offer no competing primary', async ({ page }) => {
  for (const target of ['/graphs/sdlc', '/runs/run-1/graph']) {
    await open(page, target, { width: 390, height: 844 })
    await expect(page.getByRole('group', { name: 'Схема графа' })).toBeVisible()
    expect(await overflow(page)).toEqual({ page: 0, controls: [] })
    expect(await page.locator('main button[class*="primary"]').count()).toBe(0)

    const visual = await page.locator('main a').evaluateAll((elements) => elements
      .map((element) => ({ name: element.getAttribute('aria-label') || element.textContent?.trim(), top: element.getBoundingClientRect().top, left: element.getBoundingClientRect().left }))
      .sort((a, b) => a.top - b.top || a.left - b.left)
      .map((item) => item.name))
    const dom = await page.locator('main a').evaluateAll((elements) => elements
      .map((element) => element.getAttribute('aria-label') || element.textContent?.trim()))
    expect(dom).toEqual(visual)
  }
})

test('dark theme and reduced motion keep the graph readable', async ({ page }) => {
  await open(page, '/runs/run-1/graph', { theme: 'dark', width: 390, height: 844, reducedMotion: 'reduce' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  expect(await overflow(page)).toEqual({ page: 0, controls: [] })
  const animations = await page.locator('svg [class*="scene"], svg g[class*="_edge_"], svg g[class*="_node_"], svg [class*="edgeFlow"], svg rect[class*="scanLine"], svg rect[class*="nodeRect"]')
    .evaluateAll((elements) => elements.map((element) => getComputedStyle(element).animationName))
  expect(animations.length).toBeGreaterThan(0)
  expect(animations.every((name) => name === 'none')).toBe(true)
  await expect(page.locator('svg [class*="holo"]')).toBeHidden()
})

test('flow, materialisation and scan line animate by default without blocking node links', async ({ page }) => {
  await open(page, '/runs/run-1/graph')
  const diagram = page.getByRole('group', { name: 'Схема графа' })
  await expect(diagram).toBeVisible()

  const animation = (locator) => locator.evaluate((element) => {
    const style = getComputedStyle(element)
    return { name: style.animationName, duration: style.animationDuration }
  })
  const flow = await animation(diagram.locator('[class*="edgeFlow"]').first())
  expect(flow.name).not.toBe('none')
  expect(flow.duration).not.toBe('0s')
  const dash = await diagram.locator('[class*="edgeFlow"]').first()
    .evaluate((element) => getComputedStyle(element).strokeDasharray)
  expect(dash).not.toBe('none')

  const node = await animation(diagram.locator('g[class*="_node_"]').first())
  expect(node.name).not.toBe('none')
  const scan = await animation(diagram.locator('rect[class*="scanLine"]'))
  expect(scan.name).not.toBe('none')

  // Декоративный слой не перехватывает клик: узел остаётся ссылкой.
  await expect(diagram.getByRole('link', { name: 'Открыть задачу: Сборка' })).toBeVisible()
  await diagram.getByRole('link', { name: 'Открыть задачу: Сборка' }).click()
  await expect(page).toHaveURL(/\/tasks\/run-1$/)
})
