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

// Resolve a CSS custom property to the same computed form the browser reports
// for a real element (hex var -> rgb, shadow token -> shadow list).
async function tokenValue(page, declaration, property) {
  return page.evaluate(({ declaration, property }) => {
    const node = document.createElement('span')
    node.style.cssText = `position:absolute;left:-9999px;${declaration}`
    document.body.appendChild(node)
    const value = getComputedStyle(node)[property]
    node.remove()
    return value
  }, { declaration, property })
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
      // The role hint is shown instead of the buttons the viewer may not use.
      if (id === 'developer-on-owner-stage') await expect(actions.getByTestId('role-needed')).toBeVisible()

      // No two buttons with the same name in the actions column (one meaning, one button).
      const names = (await actions.getByRole('button').allInnerTexts()).map((text) => text.replace(/\s+/g, ' ').trim()).filter(Boolean)
      expect(names, `duplicate buttons: ${names.join(' | ')}`).toEqual([...new Set(names)])
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

test.describe('real requests of the task page', () => {
  async function record(page) {
    const calls = []
    await page.route('**/api/**', async (route) => {
      const request = route.request()
      if (request.method() !== 'GET') {
        calls.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON?.() ?? null })
        return route.fulfill({ status: 200, json: { status: 'resolved', approval: { status: 'pending' } } })
      }
      return route.fallback()
    })
    return calls
  }

  test('accepting a transition goes through the decision card with the exact subject hash', async ({ page }) => {
    await page.addInitScript(() => { try { localStorage.setItem('ai-team:theme', 'light') } catch {} })
    await page.setViewportSize({ width: 1440, height: 900 })
    await installApi(page, 'awaiting-human')
    const calls = await record(page)
    await page.goto(`${baseURL}/tasks/run-audit`)
    const actions = page.getByRole('complementary', { name: 'Действия' })
    await expect(actions.getByRole('region', { name: 'Этап «Продуктовая спецификация»' })).toBeVisible()
    await actions.getByRole('button', { name: 'Принять и перейти: Техническая реализация', exact: true }).click()
    await expect.poll(() => calls.length).toBeGreaterThan(0)
    const decision = calls.find((call) => call.path === '/api/runs/run-audit/approvals/gate-product_spec/decisions')
    expect(decision, JSON.stringify(calls)).toBeTruthy()
    expect(JSON.stringify(decision.body)).toContain('"approve"')
    expect(JSON.stringify(decision.body)).toContain('cccccccccccccccc')
  })

  test('a return while a transition waits addresses the completed stage, once, without a skip', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await installApi(page, 'transition-return')
    const calls = await record(page)
    await page.goto(`${baseURL}/tasks/run-audit`)
    const actions = page.getByRole('complementary', { name: 'Действия' })
    await expect(actions.getByRole('region', { name: 'Этап «Код-ревью»' })).toBeVisible()
    await expect(actions.getByRole('button', { name: /^Вернуть на доработку/ })).toHaveCount(1)
    await expect(actions.getByRole('button', { name: 'Пропустить этап' })).toHaveCount(0)
    await actions.getByLabel('Причина возврата').fill('Не хватает тестов')
    await actions.getByRole('button', { name: 'Вернуть на доработку: Техническая реализация', exact: true }).click()
    await expect.poll(() => calls.length).toBeGreaterThan(0)
    expect(calls[0].path).toBe('/api/runs/run-audit/stages/code_review/return')
    expect(calls[0].body).toMatchObject({ to_stage: 'implementation', reason: 'Не хватает тестов' })
  })
})

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

  // The focus order is the visual order at every width: status, materials, actions. The skip link
  // «К действиям» is the first stop in the header and jumps to the actions column.
  const focusOrder = (page) => page.evaluate(() => {
    const focusable = [...document.querySelectorAll('button, a[href], input, textarea, select, summary, [tabindex="0"]')]
      .filter((element) => element.getBoundingClientRect().width > 0)
    const index = (selector) => focusable.findIndex((element) => element.matches(selector) || element.closest(selector))
    const position = (selector) => { const box = document.querySelector(selector)?.getBoundingClientRect(); return box ? { x: box.x, y: box.y } : null }
    return {
      status: index('aside[aria-label="Статус и контекст"]'), tabs: index('[role="tablist"]'), actions: index('aside[aria-label="Действия"]'),
      at: { status: position('aside[aria-label="Статус и контекст"]'), tabs: position('[role="tablist"]'), actions: position('aside[aria-label="Действия"]') },
    }
  })

  test('on a desktop the tab order is the visual order: status, materials, actions', async ({ page }) => {
    await open(page, 'awaiting-human', { width: 1600, height: 900 })
    const order = await focusOrder(page)
    expect(order.actions).toBeGreaterThan(-1)
    expect(order.status).toBeLessThan(order.tabs)
    expect(order.tabs).toBeLessThan(order.actions)
    // Visually the same: the materials sit between the status column and the actions column.
    expect(order.at.status.x).toBeLessThan(order.at.tabs.x)
    expect(order.at.tabs.x).toBeLessThan(order.at.actions.x)
  })

  test('on a phone the order is the same and the skip link reaches the actions', async ({ page }) => {
    await open(page, 'awaiting-human', { width: 390, height: 844 })
    const order = await focusOrder(page)
    expect(order.status).toBeLessThan(order.tabs)
    expect(order.tabs).toBeLessThan(order.actions)
    expect(order.at.tabs.y).toBeLessThan(order.at.actions.y)
    const skip = page.getByRole('link', { name: 'К действиям' })
    await expect(skip).toBeVisible()
    await skip.click()
    await expect(page).toHaveURL(/#task-actions$/)
  })
})

test.describe('DES-03 neon stage strip', () => {
  test('the current stage glows, the conveyor flows and the primary action glows in the dark theme', async ({ page }) => {
    await open(page, 'awaiting-human', { theme: 'dark' })
    const current = page.locator('ol li[data-state="current"] button').first()
    await expect(current).toBeVisible()
    expect(await current.evaluate((el) => getComputedStyle(el).boxShadow)).not.toBe('none')
    expect(await current.locator('span[class*="routeDot"]').evaluate((el) => getComputedStyle(el).boxShadow)).not.toBe('none')
    expect(await current.evaluate((el) => getComputedStyle(el.closest('li'), '::before').animationName)).not.toBe('none')
    const primary = page.getByRole('complementary', { name: 'Действия' }).locator('button[class*="primary"]').first()
    await expect(primary).toBeVisible()
    expect(await primary.evaluate((el) => getComputedStyle(el).boxShadow)).not.toBe('none')
  })

  test.describe('prefers-reduced-motion', () => {
    test.use({ reducedMotion: 'reduce' })

    test('the conveyor and the pulsing dot stop moving', async ({ page }) => {
      await open(page, 'awaiting-human')
      const current = page.locator('ol li[data-state="current"] button').first()
      await expect(current).toBeVisible()
      await expect.poll(() => current.evaluate((el) => getComputedStyle(el.closest('li'), '::before').animationName)).toBe('none')
      await expect.poll(() => current.locator('span[class*="routeDot"]').evaluate((el) => getComputedStyle(el).animationName)).toBe('none')
    })
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
    // The server blocks a product owner only from the administrator right itself and from revoking an administrator.
    await expect(page.getByRole('button', { name: 'Изменить роли: owner@example.com' })).toBeEnabled()
    await expect(page.getByRole('button', { name: 'Отозвать доступ: owner@example.com' })).toBeDisabled()
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

// DES-04: the animations use DES-02 tokens, keep S16 colour meanings and are
// readable in both themes. The neutral/secondary "primary" rule above still
// holds for every scenario looped through at the top of the file.
test.describe('DES-04: animated timeline and gates', () => {
  // The audit fixtures keep the event log short; the timeline needs a started
  // attempt and a mark to render the effects, so override just that endpoint.
  const timelineEvents = [
    { version: 1, cursor: 1, run_id: 'run-audit', sequence: 1, type: 'attempt_started', attempt_id: 'impl-1', stage: 'implementation', timestamp: '2026-10-12T09:00:00Z', data: { agent: 'coder' } },
    { version: 1, cursor: 2, run_id: 'run-audit', sequence: 2, type: 'check_overridden', stage: 'implementation', actor: 'root', timestamp: '2026-10-12T09:05:00Z', data: { reason: 'flaky' } },
  ]

  async function journal(page, id, theme, withTimeline = false) {
    await page.addInitScript((value) => { try { localStorage.setItem('ai-team:theme', value) } catch {} }, theme)
    await page.setViewportSize({ width: 1440, height: 900 })
    await installApi(page, id)
    if (withTimeline) await page.route('**/api/runs/run-audit/events**', (route) => route.fulfill({ status: 200, json: { events: timelineEvents, next_cursor: timelineEvents.length } }))
    await page.goto(`${baseURL}/tasks/run-audit`)
    await expect(page.getByRole('heading', { name: 'greet-by-name', level: 1 })).toBeVisible()
    await page.getByRole('tab', { name: 'Журнал' }).click()
  }

  for (const theme of ['light', 'dark']) {
    test(`${theme}: running interval, marks and active visit keep executor and state colours`, async ({ page }) => {
      await journal(page, 'working-agent', theme, true)
      const open = page.locator('[data-open]').first()
      await expect(open).toBeVisible()
      const flow = await open.evaluate((element) => ({
        stripes: getComputedStyle(element, '::before').animationName,
        duration: getComputedStyle(element, '::before').animationDuration,
        glow: getComputedStyle(element, '::after').animationName,
        shadow: getComputedStyle(element, '::after').boxShadow,
        border: getComputedStyle(element).borderTopColor,
      }))
      expect(flow.stripes).toContain('timeline-stripes')
      expect(flow.duration).toContain('0.42s')
      expect(flow.glow).toContain('timeline-glow')
      expect(flow.shadow).not.toBe('none')
      expect(flow.border).toBe(await tokenValue(page, 'border-color: var(--executor-agent)', 'borderTopColor'))

      const mark = page.locator('[data-kind][title]').first()
      await expect(mark).toBeVisible()
      expect(await mark.evaluate((element) => getComputedStyle(element).animationName)).toContain('mark-in')

      const visit = page.locator('[data-active]').first()
      await expect(visit).toBeVisible()
      const active = await visit.evaluate((element) => ({
        glow: getComputedStyle(element, '::after').animationName,
        shadow: getComputedStyle(element, '::after').boxShadow,
        background: getComputedStyle(element).backgroundColor,
      }))
      expect(active.glow).toContain('visit-glow')
      expect(active.shadow).toBe(await tokenValue(page, 'box-shadow: var(--glow-accent)', 'boxShadow'))
      expect(active.background).toBe(await tokenValue(page, 'background-color: var(--tint-indigo)', 'backgroundColor'))
    })

    test(`${theme}: override stays red, advisory amber and a technical gate dashed`, async ({ page }) => {
      await open(page, 'override', { theme })
      await page.getByRole('tab', { name: /Гейты/ }).click()
      const override = page.locator('[data-kind="override"]').first()
      await expect(override).toBeVisible()
      const red = await override.evaluate((element) => ({
        animation: getComputedStyle(element, '::before').animationName,
        shadow: getComputedStyle(element, '::before').boxShadow,
        color: getComputedStyle(element).color,
        background: getComputedStyle(element).backgroundColor,
      }))
      expect(red.animation).toContain('banner-glow')
      expect(red.shadow).toBe(await tokenValue(page, 'box-shadow: var(--glow-coral)', 'boxShadow'))
      expect(red.color).toBe(await tokenValue(page, 'color: var(--danger-fg)', 'color'))
      expect(red.background).toBe(await tokenValue(page, 'background-color: var(--danger-bg)', 'backgroundColor'))

      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await open(page, 'advisory', { theme })
      await page.getByRole('tab', { name: /Гейты/ }).click()
      const advisory = page.locator('[data-kind="advisory"]').first()
      await expect(advisory).toBeVisible()
      const amber = await advisory.evaluate((element) => ({
        animation: getComputedStyle(element, '::before').animationName,
        shadow: getComputedStyle(element, '::before').boxShadow,
        color: getComputedStyle(element).color,
        background: getComputedStyle(element).backgroundColor,
      }))
      expect(amber.animation).toContain('banner-glow')
      expect(amber.shadow).toBe(await tokenValue(page, 'box-shadow: var(--glow-accent)', 'boxShadow'))
      expect(amber.color).toBe(await tokenValue(page, 'color: var(--warning-fg)', 'color'))
      expect(amber.background).toBe(await tokenValue(page, 'background-color: var(--warning-bg)', 'backgroundColor'))

      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await open(page, 'gate-error', { theme })
      await page.getByRole('tab', { name: /Гейты/ }).click()
      const technical = page.locator('[class*="technical"]').first()
      await expect(technical).toBeVisible()
      const error = await technical.evaluate((element) => ({
        outline: getComputedStyle(element).outlineStyle,
        color: getComputedStyle(element).outlineColor,
      }))
      expect(error.outline).toBe('dashed')
      expect(error.color).toBe(await tokenValue(page, 'border-color: var(--danger-fg)', 'borderTopColor'))
    })

    test(`${theme}: reduced motion leaves every DES-04 effect static`, async ({ page }) => {
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await journal(page, 'working-agent', theme, true)
      const stopped = await page.evaluate(() => {
        const segment = document.querySelector('[data-open]')
        const mark = document.querySelector('[data-kind][title]')
        const visit = document.querySelector('[data-active]')
        return {
          stripes: getComputedStyle(segment, '::before').animationName,
          segmentGlow: getComputedStyle(segment, '::after').animationName,
          mark: getComputedStyle(mark).animationName,
          visit: getComputedStyle(visit, '::after').animationName,
          markOpacity: getComputedStyle(mark).opacity,
        }
      })
      expect(stopped).toEqual({ stripes: 'none', segmentGlow: 'none', mark: 'none', visit: 'none', markOpacity: '1' })

      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await open(page, 'override', { theme })
      await page.getByRole('tab', { name: /Гейты/ }).click()
      const banner = await page.locator('[data-kind="override"]').first().evaluate((element) => ({
        animation: getComputedStyle(element, '::before').animationName,
        opacity: getComputedStyle(element, '::before').opacity,
      }))
      expect(banner.animation).toBe('none')
      expect(Number(banner.opacity)).toBeGreaterThan(0)
    })
  }
})

// DES-05: volumetric cards, gradient status strips and shimmering pastel bubbles.
// Only DES-02 tokens decorate; S16 colours, focus order and the 390px layout stay as they were.
test.describe('DES-05: volumetric cards and pastel bubbles', () => {
  const runs = [
    { id: 1, run_id: 'des05-running', feature: 'Работающая задача', status: 'running', started_at: '2026-10-12T09:00:00Z' },
    { id: 2, run_id: 'des05-failed', feature: 'Упавшая задача', status: 'failed', started_at: '2026-10-12T09:00:00Z' },
  ]

  async function stub(page, theme, width, api) {
    await page.addInitScript((value) => { try { localStorage.setItem('ai-team:theme', value) } catch {} }, theme)
    await page.setViewportSize({ width, height: 900 })
    await page.route('**/api/**', async (route) => {
      const { pathname } = new URL(route.request().url())
      const json = (body, status = 200) => route.fulfill({ status, json: body })
      if (pathname === '/api/session') return json({ csrf_token: 'x', principal: { actor_id: 'audit-user', roles: ['product_owner'] } })
      if (pathname === '/api/auth/config') return json({ authentication_required: true, team_management_enabled: true })
      if (pathname === '/api/version') return json({ version: 'audit' })
      const custom = api?.(pathname, json)
      if (custom) return custom
      if (pathname === '/api/projects') return json({ projects: [] })
      if (pathname === '/api/preflight') return json({ ready: true, checks: [] })
      if (pathname === '/api/tracker') return json({ configured: false })
      return json({ error: `audit fixture: ${pathname} is not stubbed` }, 404)
    })
  }

  async function queue(page, theme = 'light', width = 1440) {
    await stub(page, theme, width, (pathname, json) => {
      if (pathname === '/api/pipelines') return json(runs)
    })
    await page.goto(`${baseURL}/`)
    await expect(page.getByRole('link', { name: 'Открыть задачу: Работающая задача' })).toBeVisible()
  }

  for (const theme of ['light', 'dark']) {
    test(`${theme}: queue cards layer shadows, grade the status strip and lift with a token glow`, async ({ page }) => {
      await queue(page, theme)
      const card = page.getByRole('link', { name: 'Открыть задачу: Работающая задача' })
      const base = await card.evaluate((element) => ({
        shadow: getComputedStyle(element).boxShadow,
        strip: getComputedStyle(element, '::before').backgroundImage,
        duration: getComputedStyle(element).transitionDuration,
      }))
      expect(base.shadow).not.toBe('none')
      expect(base.shadow.split(/,(?![^(]*\))/).length).toBeGreaterThanOrEqual(3)
      expect(base.strip).toContain('linear-gradient')
      expect(base.duration).toContain('0.24s')

      const tealGlow = await tokenValue(page, 'box-shadow: var(--glow-teal)', 'boxShadow')
      await card.hover()
      await expect.poll(() => card.evaluate((element) => getComputedStyle(element).boxShadow)).toContain(tealGlow)
      await expect.poll(() => card.evaluate((element) => getComputedStyle(element).transform)).toContain('-2')

      const failedStrip = await page.getByRole('link', { name: 'Открыть задачу: Упавшая задача' })
        .evaluate((element) => getComputedStyle(element, '::before').backgroundImage)
      expect(failedStrip).toContain('linear-gradient')
      expect(failedStrip).not.toBe(base.strip)
      expect((await horizontalOverflow(page)).page).toBe(0)
    })

    test(`${theme}: pastel bubbles shimmer and the active filter is softly lit`, async ({ page }) => {
      await queue(page, theme)
      // The active nav item has its own highlight from Layout; take the next bubble
      // so we observe the pastel shimmer that Bubble.module.css itself provides.
      const bubble = page.locator('nav[aria-label="Разделы"] a span[aria-hidden="true"]').nth(1)
      const pastel = await bubble.evaluate((element) => ({
        image: getComputedStyle(element).backgroundImage,
        size: getComputedStyle(element).backgroundSize,
        animation: getComputedStyle(element).animationName,
        glow: getComputedStyle(element).boxShadow,
      }))
      expect(pastel.image).toContain('linear-gradient')
      expect(pastel.size).toBe('180% 180%')
      expect(pastel.animation).toContain('bubble-sheen')
      expect(pastel.glow).toBe(await tokenValue(page, 'box-shadow: var(--shadow-soft), var(--glow-teal)', 'boxShadow'))

      const active = page.getByRole('button', { name: 'Все', pressed: true })
      const lit = await active.evaluate((element) => ({
        image: getComputedStyle(element).backgroundImage,
        shadow: getComputedStyle(element).boxShadow,
      }))
      expect(lit.image).toContain('linear-gradient')
      expect(lit.shadow).not.toBe('none')
      // The soft highlight never becomes a second primary: one yellow action at most.
      expect(await page.locator('button[class*="primary"]').count()).toBeLessThanOrEqual(1)
    })

    test(`${theme}: a board card grades its strip from the chip tone and keeps its focus ring`, async ({ page }) => {
      await stub(page, theme, 1440, (pathname, json) => {
        if (pathname === '/api/runs') return json({ runs: [{ id: 3, run_id: 'des05-board', feature: 'desk-board', task: 'Карточка доски', status: 'blocked',
          started_at: '2026-10-12T09:00:00Z', warnings: 0, allowed_actions: [] }], total: 1, limit: 50, offset: 0 })
        if (pathname === '/api/templates') return json({ templates: [], active_template: '' })
      })
      await page.goto(`${baseURL}/tasks`)
      const card = page.getByRole('link', { name: 'Задача Карточка доски' })
      await expect(card).toBeVisible()
      const strip = await card.evaluate((element) => ({
        image: getComputedStyle(element, '::before').backgroundImage,
        shadow: getComputedStyle(element).boxShadow,
      }))
      expect(strip.image).toContain('linear-gradient')
      expect(strip.shadow).not.toBe('none')
      await card.focus()
      expect(await card.evaluate((element) => getComputedStyle(element).outlineStyle)).not.toBe('none')
      await card.hover()
      await expect.poll(() => card.evaluate((element) => getComputedStyle(element).transform)).toContain('-2')
    })
  }

  test('reduced motion stops the shimmer and keeps every hover static', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await queue(page)
    const bubble = page.locator('nav[aria-label="Разделы"] a span[aria-hidden="true"]').first()
    expect(await bubble.evaluate((element) => getComputedStyle(element).animationName)).toBe('none')
    const card = page.getByRole('link', { name: 'Открыть задачу: Работающая задача' })
    await card.hover()
    await expect.poll(() => card.evaluate((element) => getComputedStyle(element).transform)).toBe('none')
  })

  test('390px keeps no horizontal scroll and the visual focus order', async ({ page }) => {
    await queue(page, 'light', 390)
    expect((await horizontalOverflow(page)).page).toBe(0)
    // On a phone the filters are collapsed; expand them to see the active item.
    await page.getByRole('button', { name: /Статус: Все · Фильтры/ }).click()
    const active = page.getByRole('button', { name: 'Все', pressed: true })
    expect(await active.evaluate((element) => getComputedStyle(element).backgroundImage)).toContain('linear-gradient')
    await page.getByRole('button', { name: /Статус: Все · Фильтры/ }).click()

    await page.getByPlaceholder('Поиск задач').focus()
    await page.keyboard.press('Tab') // фильтр по проекту
    await page.keyboard.press('Tab') // кнопка фильтров (на телефоне видима)
    await page.keyboard.press('Tab') // первая карточка
    expect(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')))
      .toBe('Открыть задачу: Работающая задача')
  })
})

// DES-06: three decorative holograms underline states that are already named in
// text. The movement borrows the reference's materialization character, never its
// pastel palette; S16 colours, focus order, actions and the 390px layout stay put.
test.describe('DES-06: holographic state glyphs', () => {
  async function emptyQueue(page, theme = 'light', width = 1440) {
    await page.addInitScript((value) => { try { localStorage.setItem('ai-team:theme', value) } catch {} }, theme)
    await page.setViewportSize({ width, height: 900 })
    await page.route('**/api/**', async (route) => {
      const { pathname } = new URL(route.request().url())
      const json = (body, status = 200) => route.fulfill({ status, json: body })
      if (pathname === '/api/session') return json({ csrf_token: 'x', principal: { actor_id: 'audit-user', roles: ['product_owner'] } })
      if (pathname === '/api/auth/config') return json({ authentication_required: true, team_management_enabled: true })
      if (pathname === '/api/version') return json({ version: 'audit' })
      if (pathname === '/api/pipelines') return json([])
      if (pathname === '/api/projects') return json({ projects: [] })
      if (pathname === '/api/preflight') return json({ ready: true, checks: [] })
      return json({ error: `audit fixture: ${pathname} is not stubbed` }, 404)
    })
    await page.goto(`${baseURL}/`)
    await expect(page.getByText(/Задач пока нет\. Нажмите «Новая задача»/)).toBeVisible()
  }

  for (const theme of ['light', 'dark']) {
    test(`${theme}: empty queue materializes a neutral reticle`, async ({ page }) => {
      await emptyQueue(page, theme)
      const glyph = page.locator('[data-holo="empty"]')
      await expect(glyph).toBeVisible()
      const shape = await glyph.evaluate((element) => ({
        color: getComputedStyle(element).color,
        arc: getComputedStyle(element.querySelector('path[class*="arc"]')).animationName,
        scan: getComputedStyle(element.querySelector('line[class*="scan"]')).animationName,
        focusable: element.querySelectorAll('button, a, [tabindex]').length,
      }))
      expect(shape.color).toBe(await tokenValue(page, 'color: var(--text-secondary)', 'color'))
      expect(shape.arc).toContain('holo-draw')
      expect(shape.scan).toContain('holo-sweep')
      expect(shape.focusable).toBe(0)
      expect(await page.locator('[data-holo="complete"], [data-holo="decision"]').count()).toBe(0)
    })

    test(`${theme}: completion draws a green check, awaiting decision orbits teal brackets`, async ({ page }) => {
      await open(page, 'passed', { theme })
      const done = page.locator('[data-holo="complete"]')
      await expect(done).toBeVisible()
      const check = await done.evaluate((element) => ({
        color: getComputedStyle(element).color,
        animation: getComputedStyle(element.querySelector('path[class*="check"]')).animationName,
        focusable: element.querySelectorAll('button, a, [tabindex]').length,
      }))
      expect(check.color).toBe(await tokenValue(page, 'color: var(--success-fg)', 'color'))
      expect(check.animation).toContain('holo-draw')
      expect(check.focusable).toBe(0)
      expect(await page.locator('[data-holo="decision"]').count()).toBe(0)
      expect(await page.getByRole('complementary', { name: 'Действия' }).locator('button[class*="primary"]').count()).toBeLessThanOrEqual(1)

      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await open(page, 'awaiting-human', { theme })
      const waiting = page.locator('[data-holo="decision"]')
      await expect(waiting).toBeVisible()
      const orbit = await waiting.evaluate((element) => ({
        color: getComputedStyle(element).color,
        animation: getComputedStyle(element.querySelector('path[class*="orbit"]')).animationName,
        focusable: element.querySelectorAll('button, a, [tabindex]').length,
      }))
      expect(orbit.color).toBe(await tokenValue(page, 'color: var(--ink-teal)', 'color'))
      expect(orbit.animation).toContain('holo-orbit')
      expect(orbit.focusable).toBe(0)
      expect(await page.locator('[data-holo="complete"]').count()).toBe(0)
      await expect(page.getByRole('button', { name: 'Принять и перейти: Техническая реализация' })).toBeVisible()
    })
  }

  test('reduced motion leaves all three glyphs static and fully drawn', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await emptyQueue(page)
    const stopped = await page.locator('[data-holo="empty"]').evaluate((element) => ({
      parts: [...element.querySelectorAll('svg *')].map((child) => getComputedStyle(child).animationName),
      filter: getComputedStyle(element.querySelector('svg')).filter,
      width: element.getBoundingClientRect().width,
    }))
    expect(stopped.parts.every((name) => name === 'none')).toBe(true)
    expect(stopped.filter).toBe('none')
    expect(stopped.width).toBeGreaterThan(0)

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await open(page, 'passed', { theme: 'dark' })
    const done = await page.locator('[data-holo="complete"] circle[class*="ring"]').evaluate((element) => ({
      animation: getComputedStyle(element).animationName,
      offset: getComputedStyle(element).strokeDashoffset,
    }))
    expect(done).toEqual({ animation: 'none', offset: '0px' })

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await open(page, 'awaiting-human')
    const orbits = await page.locator('[data-holo="decision"] path[class*="orbit"]').evaluateAll((elements) =>
      elements.map((element) => getComputedStyle(element).animationName))
    expect(orbits).toEqual(['none', 'none'])
  })

  test('390px keeps every glyph visible without horizontal scroll or a new tab stop', async ({ page }) => {
    await emptyQueue(page, 'light', 390)
    expect((await horizontalOverflow(page)).page).toBe(0)
    const empty = page.locator('[data-holo="empty"]')
    await expect(empty).toBeVisible()
    expect(await empty.evaluate((element) => element.getBoundingClientRect().width)).toBeLessThanOrEqual(44)

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await open(page, 'awaiting-human', { width: 390, height: 844 })
    expect((await horizontalOverflow(page)).page).toBe(0)
    await expect(page.locator('[data-holo="decision"]')).toBeVisible()
    // The glyph is aria-hidden and carries no tabindex: tabbing still starts at the link in the header.
    await page.getByRole('link', { name: 'К действиям' }).focus()
    await page.keyboard.press('Tab')
    expect(await page.evaluate(() => document.activeElement?.hasAttribute('data-holo'))).toBe(false)
  })
})
