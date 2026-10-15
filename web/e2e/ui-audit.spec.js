// UI audit (S16): every task state of S11 and the S06/S14/S15 extras on the real built frontend.
// The API is stubbed from audit-fixtures.js; no Go server is needed, only web/dist.
//
// Screenshots for docs/ui-audit/ are written when UI_AUDIT_DIR is set:
//   UI_AUDIT_DIR=../docs/ui-audit npx playwright test e2e/ui-audit.spec.js
import { createServer } from 'node:http'
import { mkdir, readFile, stat } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'
import { installApi, scenarios } from './audit-fixtures.js'

const webDir = path.dirname(fileURLToPath(import.meta.url)).replace(/\/e2e$/, '')
const dist = path.join(webDir, 'dist')
const shotsDir = process.env.UI_AUDIT_DIR ? path.resolve(webDir, process.env.UI_AUDIT_DIR) : ''
const types = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json' }

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
  if (shotsDir) await mkdir(shotsDir, { recursive: true })
})

test.afterAll(async () => {
  await new Promise((resolve) => server.close(resolve))
})

async function open(page, id, { theme = 'light', width = 1440, height = 900, ...options } = {}) {
  await page.addInitScript((value) => { try { localStorage.setItem('ai-team:theme', value) } catch {} }, theme)
  await page.setViewportSize({ width, height })
  await installApi(page, id, options)
  await page.goto(`${baseURL}/tasks/run-audit`)
  await expect(page.getByRole('heading', { name: 'greet-by-name', level: 1 })).toBeVisible()
  const tab = scenarios[id].tab
  if (tab) await page.getByRole('tab', { name: tab }).click()
  await expect(page.getByRole('complementary', { name: 'Действия' })).toBeVisible()
}

async function shot(page, name) {
  if (!shotsDir) return
  await page.screenshot({ path: path.join(shotsDir, `${name}.png`), animations: 'disabled' })
}

async function horizontalOverflow(page) {
  return page.evaluate(() => ({
    page: document.documentElement.scrollWidth - window.innerWidth,
    controls: [...document.querySelectorAll('button, a, input, textarea, select')].filter((element) => {
      const bounds = element.getBoundingClientRect()
      return bounds.width > 0 && (bounds.left < -1 || bounds.right > window.innerWidth + 1)
    }).map((element) => `${element.tagName}.${element.className}`),
  }))
}

// Dark and phone variants are shot for the states that differ most; light desktop for all of them.
const keyStates = ['awaiting-human', 'override', 'blocked-gate', 'tracker-conflict']

for (const [id, scenario] of Object.entries(scenarios)) {
  test.describe(`state ${id}: ${scenario.title}`, () => {
    test('shows where the task is, who acts, what is checked and the next allowed action on one screen', async ({ page }) => {
      await open(page, id)
      const context = page.getByRole('complementary', { name: 'Статус и контекст' })
      const actions = page.getByRole('complementary', { name: 'Действия' })
      const { expect: want } = scenario

      await expect(context.getByText('Где задача')).toBeVisible()
      if (!want.noActions) await expect(context.getByText('Кто действует')).toBeVisible()
      if (want.who) await expect(context).toContainText(want.who)
      if (want.chip) await expect(page.getByText(want.chip, { exact: true }).first()).toBeVisible()
      for (const name of want.mainButtons ?? []) await expect(page.getByRole('tabpanel').getByRole('button', { name, exact: true })).toBeVisible()
      if (want.text) await expect(page.getByText(want.text, { exact: false }).first()).toBeVisible()
      for (const name of want.buttons ?? []) await expect(actions.getByRole('button', { name, exact: true })).toBeVisible()
      for (const name of want.notButtons ?? []) await expect(actions.getByRole('button', { name })).toHaveCount(0)
      for (const name of want.noButtons ?? []) await expect(actions.getByRole('button', { name })).toHaveCount(0)
      if (want.hint) await expect(actions).toContainText(want.hint)
      if (want.trackerHint) await expect(actions).toContainText(want.trackerHint)
      if (want.flag) await expect(page.locator(`[data-gate-flag="${want.flag}"]`).first()).toBeVisible()
      if (want.noActions) await expect(actions.getByTestId('no-actions')).toBeVisible()

      // One primary action at most: not two competing yellow buttons.
      const primaries = await actions.locator('button[class*="primary"]').count()
      expect(primaries).toBeLessThanOrEqual(1)
      // Nothing of the log, artifacts or reports is inside the actions column.
      await expect(actions.getByRole('tablist')).toHaveCount(0)
      await shot(page, `${id}-light-desktop`)
    })

    test('fits a 390px phone without horizontal scroll', async ({ page }) => {
      await open(page, id, { width: 390, height: 844 })
      expect(await horizontalOverflow(page)).toEqual({ page: 0, controls: [] })
      if (keyStates.includes(id)) await shot(page, `${id}-light-mobile`)
    })

    if (keyStates.includes(id)) {
      test('renders in the dark theme', async ({ page }) => {
        await open(page, id, { theme: 'dark' })
        await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
        await shot(page, `${id}-dark-desktop`)
        await page.setViewportSize({ width: 390, height: 844 })
        expect(await horizontalOverflow(page)).toEqual({ page: 0, controls: [] })
        await shot(page, `${id}-dark-mobile`)
      })
    }
  })
}

test.describe('stable columns', () => {
  test('the actions column does not grow when the log, artifacts and events are huge', async ({ page }) => {
    await open(page, 'awaiting-human', { events: 3, artifacts: 0 })
    const actions = page.getByRole('complementary', { name: 'Действия' })
    const small = await actions.evaluate((element) => ({ height: element.getBoundingClientRect().height, scroll: element.scrollHeight }))
    await page.getByRole('tab', { name: 'Журнал' }).click()
    const pageHeightSmall = await page.evaluate(() => document.documentElement.scrollHeight)

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await open(page, 'awaiting-human', { events: 1500, artifacts: 400 })
    await page.getByRole('tab', { name: 'Журнал' }).click()
    const big = await page.getByRole('complementary', { name: 'Действия' }).evaluate((element) => ({ height: element.getBoundingClientRect().height, scroll: element.scrollHeight }))
    const pageHeightBig = await page.evaluate(() => document.documentElement.scrollHeight)

    // The content went to the main area: the page is much longer, the actions column is the same.
    expect(pageHeightBig).toBeGreaterThan(pageHeightSmall + 2000)
    expect(big.height).toBe(small.height)
    expect(big.scroll).toBe(small.scroll)
    shotsDir && await shot(page, 'stable-actions-column-huge-log')
  })

  test('the actions column keeps its own height and scrolls inside when a plan is long', async ({ page }) => {
    await open(page, 'awaiting-human', { width: 1440, height: 700 })
    const actions = page.getByRole('complementary', { name: 'Действия' })
    const style = await actions.evaluate((element) => ({ position: getComputedStyle(element).position, overflowY: getComputedStyle(element).overflowY, max: getComputedStyle(element).maxHeight }))
    expect(style.position).toBe('sticky')
    expect(style.overflowY).toBe('auto')
    expect(style.max).not.toBe('none')
  })

  test('the left column names the state and the right one only offers actions', async ({ page }) => {
    await open(page, 'awaiting-human')
    const context = page.getByRole('complementary', { name: 'Статус и контекст' })
    const actions = page.getByRole('complementary', { name: 'Действия' })
    expect(await context.getByRole('button').count()).toBe(0)
    expect(await context.getByRole('textbox').count()).toBe(0)
    expect(await actions.getByRole('button').count()).toBeGreaterThan(0)
    const [contextBox, actionsBox] = await Promise.all([context.boundingBox(), actions.boundingBox()])
    expect(contextBox.x).toBeLessThan(actionsBox.x)
  })
})

test.describe('keyboard and focus', () => {
  test('tabs are reachable and operable from the keyboard with a visible focus ring', async ({ page }) => {
    await open(page, 'awaiting-human')
    const first = page.getByRole('tab', { name: 'Результат' })
    await first.focus()
    await page.keyboard.press('ArrowRight')
    const gates = page.getByRole('tab', { name: /Гейты/ })
    await expect(gates).toBeFocused()
    await expect(gates).toHaveAttribute('aria-selected', 'true')
    const outline = await gates.evaluate((element) => getComputedStyle(element).outlineStyle)
    expect(outline).not.toBe('none')
    await page.keyboard.press('Tab')
    await expect(page.getByRole('tabpanel')).toBeFocused()
  })

  test('the decision buttons are in the tab order after the status column and before the materials', async ({ page }) => {
    await open(page, 'awaiting-human', { width: 390, height: 844 })
    const order = await page.evaluate(() => {
      const focusable = [...document.querySelectorAll('button, a[href], input, textarea, select, summary, [tabindex="0"]')]
        .filter((element) => element.getBoundingClientRect().width > 0)
      const index = (selector) => focusable.findIndex((element) => element.matches(selector) || element.closest(selector))
      return { route: index('[aria-label="Маршрут процесса"], section[class*="routePanel"]'), actions: index('aside[aria-label="Действия"]'), tabs: index('[role="tablist"]') }
    })
    expect(order.actions).toBeGreaterThan(-1)
    expect(order.actions).toBeLessThan(order.tabs)
  })
})

test.describe('empty and error states', () => {
  async function loggedIn(page, handlers) {
    await page.addInitScript(() => { try { localStorage.setItem('ai-team:theme', 'light') } catch {} })
    await page.route('**/api/**', async (route) => {
      const { pathname, searchParams } = new URL(route.request().url())
      const json = (body, status = 200) => route.fulfill({ status, json: body })
      if (pathname === '/api/session') return json({ csrf_token: 'x', principal: { actor_id: 'audit-user', roles: handlers.roles ?? ['product_owner'] } })
      if (pathname === '/api/auth/config') return json({ authentication_required: true, team_management_enabled: true })
      if (pathname === '/api/version') return json({ version: 'audit' })
      const custom = handlers.api?.(pathname, searchParams, json)
      if (custom) return custom
      if (pathname === '/api/tracker') return json({ configured: false })
      return json({ error: `audit fixture: ${pathname} is not stubbed` }, 404)
    })
  }

  test('queue: a failed load says so and offers a retry, an empty queue names the first step', async ({ page }) => {
    let failing = true
    await loggedIn(page, { api: (pathname, _query, json) => {
      if (pathname === '/api/pipelines') return failing ? json({ error: 'база недоступна' }, 500) : json([])
      if (pathname === '/api/projects') return json({ projects: [] })
      if (pathname === '/api/preflight') return json({ ready: true, checks: [] })
    } })
    await page.goto(`${baseURL}/`)
    await expect(page.getByRole('alert')).toContainText('база недоступна')
    failing = false
    await page.getByRole('button', { name: 'Повторить загрузку' }).click()
    await expect(page.getByText(/Задач пока нет\. Нажмите «Новая задача»/)).toBeVisible()
    await shot(page, 'queue-empty-light-desktop')
  })

  test('tasks: an empty installation explains what a task is and keeps the stage board visible', async ({ page }) => {
    await loggedIn(page, { api: (pathname, _query, json) => {
      if (pathname === '/api/runs') return json({ runs: [], total: 0, limit: 50, offset: 0 })
      if (pathname === '/api/projects') return json({ projects: [{ id: 1, name: 'Основной', description: '', archived: false, created_at: '', updated_at: '' }] })
      if (pathname === '/api/templates') return json({ templates: [{ valid: true, template: 'idea-to-prod', title: 'От идеи до продакшена', version: 'a'.repeat(64), stages: [
        { id: 'intent', title: 'Бизнес-идея', function: 'po', result: 'approve', executor: 'human' }], returns: [] }], active_template: 'idea-to-prod' })
    } })
    await page.goto(`${baseURL}/tasks`)
    await expect(page.getByLabel('Задач пока нет')).toContainText('Нажмите «Новая задача»')
    await expect(page.getByLabel('Этап: Бизнес-идея')).toBeVisible()
    await shot(page, 'tasks-empty-light-desktop')
  })

  test('task page: an API error is shown as an alert, not an empty page', async ({ page }) => {
    await loggedIn(page, { api: (pathname, _query, json) => {
      if (pathname === '/api/runs/missing') return json({ error: 'задача не найдена' }, 404)
    } })
    await page.goto(`${baseURL}/tasks/missing`)
    await expect(page.getByRole('alert')).toContainText('задача не найдена')
  })

  test('new task: a role without the right is told who may create tasks', async ({ page }) => {
    await loggedIn(page, { roles: ['developer'], api: (pathname, _query, json) => {
      if (pathname === '/api/projects') return json({ projects: [] })
      if (pathname === '/api/templates') return json({ templates: [], active_template: '' })
    } })
    await page.goto(`${baseURL}/tasks/new`)
    await expect(page.getByText('Создавать задачи могут: Владелец продукта, Архитектор.')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Команда' })).toHaveCount(0)
  })

  test('team: the admin role can be granted only by an administrator', async ({ page }) => {
    const members = [
      { actor_id: 'owner@example.com', email: 'owner@example.com', roles: ['product_owner', 'admin'], status: 'active', updated_at: '' },
      { actor_id: 'qa@example.com', email: 'qa@example.com', roles: ['qa'], status: 'active', updated_at: '' },
    ]
    await loggedIn(page, { roles: ['product_owner', 'admin'], api: (pathname, _query, json) => {
      if (pathname === '/api/team/members') return json({ members })
      if (pathname === '/api/team/audit') return json({ events: [{ id: 1, actor_id: 'owner@example.com', action: 'roles_changed', target_actor_id: 'qa@example.com', details: {}, created_at: '2026-10-12T09:00:00Z' }] })
    } })
    await page.goto(`${baseURL}/team`)
    await page.getByRole('button', { name: 'Изменить роли: qa@example.com' }).click()
    const editor = page.getByRole('group', { name: 'Роли: qa@example.com' })
    await expect(editor.getByRole('checkbox', { name: /Администратор/ })).toBeEnabled()
    await shot(page, 'team-roles-light-desktop')
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await loggedIn(page, { roles: ['product_owner'], api: (pathname, _query, json) => {
      if (pathname === '/api/team/members') return json({ members })
      if (pathname === '/api/team/audit') return json({ events: [] })
    } })
    await page.goto(`${baseURL}/team`)
    await page.getByRole('button', { name: 'Изменить роли: qa@example.com' }).click()
    await expect(page.getByRole('group', { name: 'Роли: qa@example.com' }).getByRole('checkbox', { name: /Администратор/ })).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Изменить роли: owner@example.com' })).toBeDisabled()
  })

  test('tracker overview lists open requests and failed deliveries', async ({ page }) => {
    const request = { tracker: 'fake', change_id: 'c1', task_id: 7, issue_key: 'DEMO-1', from_status: 'In Progress', to_status: 'Done', origin: 'external', changed_at: '2026-10-12T09:00:00Z',
      state: 'conflict', action: 'advance', reason: 'этап ждёт решения человека', detected_at: '', updated_at: '' }
    await loggedIn(page, { roles: ['architect'], api: (pathname, _query, json) => {
      if (pathname === '/api/tracker') return json({ configured: true, tracker: 'fake', failed_deliveries: 1, link_conflicts: 0,
        inbound: { configured: true, poll_interval_seconds: 60, status: 'ok', delay_seconds: 12, pending_requests: 0, conflicts: 1 } })
      if (pathname === '/api/tracker/inbound') return json({ requests: [request] })
      if (pathname === '/api/tracker/deliveries') return json({ deliveries: [{ event_id: 3, task_id: 7, tracker: 'fake', issue_key: 'DEMO-1', event_type: 'attempt_started',
        stage: 'implementation', status: 'failed', attempts: 2, last_error: 'tracker: 503', created_at: '', updated_at: '' }] })
    } })
    await page.goto(`${baseURL}/tracker`)
    await expect(page.getByRole('heading', { name: /Открытые входящие запросы \(1\)/ })).toBeVisible()
    await expect(page.getByRole('heading', { name: /Не доставлено в трекер \(1\)/ })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Трекер' })).toBeVisible()
    await shot(page, 'tracker-overview-light-desktop')
  })

  test('MCP access page: tokens and the one-time secret', async ({ page }) => {
    await loggedIn(page, { api: (pathname, _query, json) => {
      if (pathname === '/api/mcp/tokens') return json({ max_active: 5, tokens: [{ id: 't1', label: 'Claude Code', hint: 'a1b2', created_at: '2026-10-12T09:00:00Z', expires_at: '2027-01-12T09:00:00Z' }] })
    } })
    await page.goto(`${baseURL}/access`)
    await expect(page.getByRole('heading', { name: 'Доступ MCP' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Отозвать токен «Claude Code»' })).toBeVisible()
    await shot(page, 'mcp-access-light-desktop')
  })
})
