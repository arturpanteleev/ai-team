import { spawn, spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'

const mobileFeature = 'mobileunbrokenfeaturetoken'.repeat(3).slice(0, 64)

const webDir = path.dirname(fileURLToPath(import.meta.url)).replace(/\/e2e$/, '')
const repoDir = path.resolve(webDir, '..')

async function unusedPort() {
  const server = net.createServer()
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address()
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  return port
}

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

async function expectNoHorizontalOverflow(page, width) {
  await page.setViewportSize({ width, height: 800 })
  await expect.poll(() => page.evaluate(() => {
    const controlsOutside = [...document.querySelectorAll('button, input, textarea, a')]
      .filter((element) => {
        const style = getComputedStyle(element)
        return style.display !== 'none' && style.visibility !== 'hidden' && element.getBoundingClientRect().width > 0
      })
      .flatMap((element) => {
        const filters = element.closest('#pipeline-status-filters')
        if (filters && getComputedStyle(filters).overflowX === 'auto') return []
        const bounds = element.getBoundingClientRect()
        if (bounds.left >= -1 && bounds.right <= window.innerWidth + 1) return []
        const name = typeof element.className === 'string' && element.className ? `.${element.className.split(/\s+/).join('.')}` : ''
        return [`${element.tagName.toLowerCase()}${name}: ${bounds.left.toFixed(1)}..${bounds.right.toFixed(1)}px`]
      })
    const controlsFit = controlsOutside.length === 0
    const overflowingContent = [...document.querySelectorAll('body *')].flatMap((element) => {
      const bounds = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      if (style.display === 'none' || style.visibility === 'hidden' || bounds.width === 0) return []
      const scrollArea = element.closest('#pipeline-status-filters, .markdown pre, .markdown table, pre.log')
      if (scrollArea?.matches('#pipeline-status-filters') || (scrollArea && ['auto', 'scroll'].includes(getComputedStyle(scrollArea).overflowX))) return []
      if (element.scrollWidth <= element.clientWidth + 1) return []
      const name = typeof element.className === 'string' && element.className ? `.${element.className.split(/\s+/).join('.')}` : ''
      return [`${element.tagName.toLowerCase()}${name}: ${element.scrollWidth}px > ${element.clientWidth}px`]
    })
    return {
      controlsFit,
      controlsOutside,
      pageFits: document.documentElement.scrollWidth <= window.innerWidth && document.body.scrollWidth <= window.innerWidth,
      overflowingContent,
    }
  })).toEqual({ controlsFit: true, controlsOutside: [], pageFits: true, overflowingContent: [] })
}

async function expectFingerTargets(page, selectors) {
  const tooSmall = await page.evaluate((targetSelectors) => {
    const selector = targetSelectors.join(', ')
    return [...document.querySelectorAll(selector)].flatMap((element) => {
      const style = getComputedStyle(element)
      const bounds = element.getBoundingClientRect()
      if (style.display === 'none' || style.visibility === 'hidden' || bounds.width === 0) return []
      if (bounds.width >= 44 && bounds.height >= 44) return []
      return [`${element.tagName.toLowerCase()}: ${bounds.width.toFixed(1)}×${bounds.height.toFixed(1)}px`]
    })
  }, selectors)
  expect(tooSmall).toEqual([])
}

async function expectQuestionContrast(page, width) {
  await page.setViewportSize({ width, height: 800 })
  const ratios = await page.evaluate(() => {
    const luminance = (color) => {
      const [red, green, blue] = color.match(/[\d.]+/g).slice(0, 3).map(Number).map((channel) => {
        const value = channel / 255
        return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
      })
      return 0.2126 * red + 0.7152 * green + 0.0722 * blue
    }
    const contrast = (foreground, background) => {
      const values = [luminance(foreground), luminance(background)].sort((left, right) => right - left)
      return (values[0] + 0.05) / (values[1] + 0.05)
    }
    const cards = [...document.querySelectorAll('section[class*="controls"] [class*="question"]')]
      .filter((element) => !element.className.includes('questionText'))
    return cards.flatMap((card) => {
      const background = getComputedStyle(card).backgroundColor
      const text = [contrast(getComputedStyle(card).color, background)]
      for (const element of card.querySelectorAll('a, code')) text.push(contrast(getComputedStyle(element).color, background))
      for (const element of card.querySelectorAll('textarea')) {
        text.push(contrast(getComputedStyle(element).color, getComputedStyle(element).backgroundColor))
      }
      return text
    })
  })
  expect(ratios.length).toBeGreaterThan(0)
  expect(ratios.every((ratio) => ratio >= 4.5)).toBe(true)
}

test('real React dashboard shows durable scheduler queue, worker failure, reload, and queued cancel', async ({ page }) => {
  const bubblewrapBinary = process.env.PATH.split(path.delimiter)
    .map((directory) => path.join(directory, 'bwrap'))
    .find(existsSync)
  test.skip(!bubblewrapBinary, 'trusted terminal recovery requires Linux bubblewrap')
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-browser-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  let webServer
  let worker
  const browserErrors = []

  try {
    run('go', ['build', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    await mkdir(target, { recursive: true })
    run(binary, ['init', '--target', target], { cwd: repoDir })
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist'), '--scheduler-db', '.ai-team/scheduler.db'], { cwd: target })
    await waitForServer(baseURL, webServer)

    page.on('pageerror', (error) => browserErrors.push(`pageerror: ${error.message}`))
    page.on('console', (message) => {
      if (message.type() === 'error') browserErrors.push(`console: ${message.text()}`)
    })
    let pipelineListReads = 0
    page.on('request', (request) => {
      if (request.method() === 'GET' && /\/api\/pipelines\?/.test(request.url())) pipelineListReads += 1
    })
    await page.setViewportSize({ width: 360, height: 800 })
    await page.goto(baseURL)
    const localToken = (await readFile(path.join(target, '.ai-team', 'web.token'), 'utf8')).trim()
    await page.getByLabel('Токен доступа').fill(localToken)
    await page.getByRole('button', { name: 'Войти' }).click()
    await expect(page.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    for (const width of [1280, 360, 390, 430]) await expectNoHorizontalOverflow(page, width)
    for (const width of [360, 390, 430]) {
      await page.setViewportSize({ width, height: 800 })
      await expectFingerTargets(page, ['button', 'input', 'textarea', 'nav a'])
    }
    await expectNoHorizontalOverflow(page, 360)

    const filterToggle = page.getByRole('button', { name: /Статус: Все · Фильтры/ })
    await expect(filterToggle).toBeVisible()
    await filterToggle.click()
    await expect(filterToggle).toHaveAttribute('aria-expanded', 'true')
    await page.getByRole('button', { name: 'В очереди', exact: true }).click()
    await expect(page.getByRole('button', { name: /Статус: В очереди/ })).toHaveAttribute('aria-expanded', 'false')
    const readsBeforeReturn = pipelineListReads
    await page.evaluate(() => {
      const descriptor = Object.getOwnPropertyDescriptor(document, 'visibilityState')
      let simulatedState = 'visible'
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => simulatedState })
      window.__simulateVisibility = (state) => {
        simulatedState = state
        document.dispatchEvent(new Event('visibilitychange'))
        return document.visibilityState
      }
      window.__restoreVisibility = () => {
        if (descriptor) Object.defineProperty(document, 'visibilityState', descriptor)
        else delete document.visibilityState
        delete window.__simulateVisibility
        delete window.__restoreVisibility
      }
    })
    try {
      expect(await page.evaluate(() => window.__simulateVisibility('hidden'))).toBe('hidden')
      await page.waitForTimeout(200)
      expect(pipelineListReads).toBe(readsBeforeReturn)
      expect(await page.evaluate(() => window.__simulateVisibility('visible'))).toBe('visible')
      await expect.poll(() => pipelineListReads).toBeGreaterThan(readsBeforeReturn)
    } finally {
      await page.evaluate(() => window.__restoreVisibility())
    }
    await page.getByRole('button', { name: /Статус: В очереди · Фильтры/ }).click()
    await page.getByRole('button', { name: 'Все', exact: true }).click()

    await page.getByLabel('Название инициативы').fill(mobileFeature)
    await page.getByLabel('Какого результата хотите достичь?').fill('Wait for a real scheduler worker')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()

    const firstRun = page.locator('[class*="card"]').filter({ hasText: mobileFeature })
    await expect(firstRun.getByText('в очереди', { exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(firstRun.getByText(/Очередь №\d+/)).toBeVisible()
    for (const width of [360, 390, 430]) {
      await expectNoHorizontalOverflow(page, width)
      const feature = page.locator('span[class*="feature"]').filter({ hasText: mobileFeature })
      await expect(feature).toHaveCSS('overflow-wrap', 'anywhere')
      await expect.poll(() => feature.evaluate((element) => {
        const bounds = element.getBoundingClientRect()
        const style = getComputedStyle(element)
        const card = element.parentElement.parentElement
        const cardBounds = card.getBoundingClientRect()
        const cardStyle = getComputedStyle(card)
        const right = cardBounds.right - Number.parseFloat(cardStyle.borderRightWidth) - Number.parseFloat(cardStyle.paddingRight)
        return bounds.height > Number.parseFloat(style.fontSize) * 1.5 && bounds.left >= cardBounds.left && bounds.right <= right + 1
      })).toBe(true)
    }

    // Hold the claimed run in running until the browser has observed it. A
    // fixed sleep can expire between dashboard polls, making this transient
    // state disappear before the assertion gets a chance to see it.
    const wrapper = path.join(tempRoot, 'worker-wrapper.sh')
    const releaseWorker = path.join(tempRoot, 'release-worker')
    await writeFile(wrapper, `#!/bin/sh\nwhile [ ! -f '${releaseWorker}' ]; do /bin/sleep 0.05; done\nexec '${binary}' "$@"\n`, { mode: 0o755 })
    // Admission now checks the repository before launching the worker. Keep
    // Git available while leaving the runtime executable (opencode) absent,
    // so the test still reaches the intended deterministic preflight failure.
    const gitBinary = process.env.PATH.split(path.delimiter).map((directory) => path.join(directory, 'git')).find(existsSync)
    if (!gitBinary) throw new Error('git executable is required for scheduler admission')
    await symlink(gitBinary, path.join(tempRoot, 'git'))
    await symlink(bubblewrapBinary, path.join(tempRoot, 'bwrap'))
    worker = start(binary, ['scheduler-worker', '--target', target, '--scheduler-db', '.ai-team/scheduler.db', '--worker-command', wrapper, '--worker-id', 'browser-e2e-worker', '--once', '--poll-interval', '50ms'], {
      cwd: target,
      env: { ...process.env, PATH: tempRoot, AI_TEAM_WORKER_SANDBOX: 'bubblewrap' },
    })

    await expect(firstRun.getByText('в работе', { exact: true })).toBeVisible({ timeout: 10_000 })
    await writeFile(releaseWorker, 'continue')
    await expect(firstRun.getByText('ошибка', { exact: true })).toBeVisible({ timeout: 20_000 })
    await expect(firstRun.getByRole('alert')).toContainText(/preflight|opencode/i)
    await page.reload()
    const recoveredFirstRun = page.locator('[class*="card"]').filter({ hasText: mobileFeature })
    await expect(recoveredFirstRun.getByText('ошибка', { exact: true })).toBeVisible()
    await expect(recoveredFirstRun.getByRole('alert')).toContainText(/preflight|opencode/i)

    await page.getByLabel('Название инициативы').fill('browser-cancel-after-reload')
    await page.getByLabel('Какого результата хотите достичь?').fill('Remain queued until cancellation')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const queuedRun = page.locator('[class*="card"]').filter({ hasText: 'browser-cancel-after-reload' })
    await expect(queuedRun.getByText('в очереди', { exact: true })).toBeVisible()
    await expect(queuedRun.getByText(/Очередь №\d+/)).toBeVisible()

    await page.reload()
    const recoveredQueuedRun = page.locator('[class*="card"]').filter({ hasText: 'browser-cancel-after-reload' })
    await expect(recoveredQueuedRun.getByText('в очереди', { exact: true })).toBeVisible()
    await recoveredQueuedRun.getByRole('button', { name: 'Отменить' }).click()
    await expect(recoveredQueuedRun.getByText('отменена', { exact: true })).toBeVisible()
    expect(browserErrors).toEqual([])
  } finally {
    await stop(worker)
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})

test('authenticated cookie session survives reload and a second window for write commands', async ({ browser }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-auth-browser-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  const secret = 'browser-e2e-signing-secret-that-is-long-enough'
  let webServer

  try {
    run('go', ['build', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    const token = run(binary, ['auth-token', '--actor', 'browser-product-owner', '--roles', 'product_owner'], {
      cwd: repoDir,
      env: { ...process.env, AI_TEAM_AUTH_SECRET: secret },
    }).trim()
    await mkdir(target, { recursive: true })
    run(binary, ['init', '--target', target], { cwd: repoDir })
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist'), '--scheduler-db', '.ai-team/scheduler.db'], {
      cwd: target,
      env: { ...process.env, AI_TEAM_AUTH_SECRET: secret },
    })
    await waitForServer(baseURL, webServer)

    const context = await browser.newContext()
    const first = await context.newPage()
    await first.goto(baseURL)
    await first.getByLabel('Токен доступа').fill(token)
    await first.getByRole('button', { name: 'Войти' }).click()
    await expect(first.getByRole('heading', { name: 'Задачи' })).toBeVisible()

    // A new JS context has no in-memory CSRF token; the HttpOnly cookie must
    // recover it before the command is sent.
    await first.reload()
    await expect(first.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    const second = await context.newPage()
    await second.goto(baseURL)
    await expect(second.getByRole('heading', { name: 'Задачи' })).toBeVisible()

    // Team read APIs use the authenticated same-origin session without a
    // write CSRF header. The owner can load members/audit and the UI refuses
    // an empty role edit before sending a PATCH.
    await second.getByRole('link', { name: 'Команда' }).click()
    await expect(second.getByRole('heading', { name: 'Команда' })).toBeVisible()
    await expect(second.getByRole('heading', { name: 'Участники' })).toBeVisible()
    await expect(second.getByRole('heading', { name: 'Журнал действий' })).toBeVisible()
    let emptyRolePatchSent = false
    second.on('request', (request) => {
      if (request.method() === 'PATCH' && request.url().includes('/api/team/members/')) emptyRolePatchSent = true
    })
    second.once('dialog', (dialog) => dialog.accept(' , '))
    await second.getByRole('button', { name: 'Изменить роли' }).first().click()
    await expect(second.getByRole('alert')).toHaveText('Укажите хотя бы одну роль.')
    expect(emptyRolePatchSent).toBe(false)

    await second.getByRole('link', { name: 'Задачи' }).click()
    await expect(second.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    await second.getByLabel('Название инициативы').fill('authenticated-after-reconnect')
    await second.getByLabel('Какого результата хотите достичь?').fill('Submit with a recovered CSRF token')
    await second.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const runCard = second.locator('[class*="card"]').filter({ hasText: 'authenticated-after-reconnect' })
    await expect(runCard.getByText('в очереди', { exact: true })).toBeVisible({ timeout: 10_000 })

    // Browser sessions are intentionally process-local. Restarting the server
    // revokes that session while retaining the browser's HttpOnly cookie.
    await stop(webServer)
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist'), '--scheduler-db', '.ai-team/scheduler.db'], {
      cwd: target,
      env: { ...process.env, AI_TEAM_AUTH_SECRET: secret },
    })
    await waitForServer(baseURL, webServer)
    await second.reload()
    await expect(second.getByLabel('Токен доступа')).toBeVisible()
    await expect(second.getByText(/Сессия истекла или отсутствует/)).toBeVisible()
    await context.close()
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})


test('Chromium viewport flow reads, decides, resumes and reloads the persisted initiative', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-mobile-browser-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const mockBin = path.join(tempRoot, 'mockbin')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  let webServer
  const websocketUrls = []
  const websocketFrames = []
  const apiResponses = []
  let pipelineListReads = 0

  try {
    run('go', ['build', '-tags=e2etest', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    await mkdir(target, { recursive: true })
    run('git', ['init', '-b', 'main'], { cwd: target })
    run('git', ['config', 'user.name', 'AI Team Browser E2E'], { cwd: target })
    run('git', ['config', 'user.email', 'ai-team-browser-e2e@example.test'], { cwd: target })
    run('git', ['remote', 'add', 'origin', 'https://example.test/ai-team/browser-fixture.git'], { cwd: target })
    await writeFile(path.join(target, 'README.md'), 'Browser E2E fixture.\n')
    run('git', ['add', 'README.md'], { cwd: target })
    run('git', ['commit', '-m', 'Initialize browser E2E target'], { cwd: target })
    // Initialize only after Git exists so ai-team can put .ai-team/ in the
    // repository-local exclude file. The first admitted run then starts from
    // a genuinely clean workspace, matching the server's real preflight.
    run(binary, ['init', '--target', target], { cwd: repoDir })
    expect(run('git', ['status', '--porcelain', '--untracked-files=all'], { cwd: target })).toBe('')

    await mkdir(mockBin, { recursive: true })
    await symlink(path.join(repoDir, 'e2etest', 'mock-opencode.sh'), path.join(mockBin, 'opencode'))
    await writeFile(path.join(mockBin, 'gh'), '#!/bin/sh\nif [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo authenticated; exit 0; fi\nexit 1\n', { mode: 0o755 })
    const serverEnv = {
      ...process.env,
      PATH: `${mockBin}${path.delimiter}${process.env.PATH ?? ''}`,
      AI_TEAM_E2E_IN_MEMORY_LEGACY_RUNTIME: '1',
    }
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist')], {
      cwd: target,
      env: serverEnv,
    })
    await waitForServer(baseURL, webServer)

    page.on('request', (request) => {
      if (request.method() === 'GET' && new URL(request.url()).pathname === '/api/pipelines') pipelineListReads += 1
    })
    page.on('response', (response) => {
      const request = response.request()
      const url = new URL(response.url())
      if (url.pathname.startsWith('/api/')) {
        apiResponses.push({ method: request.method(), path: url.pathname, status: response.status() })
      }
    })
    page.on('websocket', (socket) => {
      websocketUrls.push(socket.url())
      socket.on('framereceived', ({ payload }) => {
        try {
          websocketFrames.push({ url: socket.url(), event: JSON.parse(typeof payload === 'string' ? payload : payload.toString()) })
        } catch {
          // Ignore non-JSON frames; the application only consumes JSON events.
        }
      })
    })

    await page.setViewportSize({ width: 360, height: 800 })
    await page.goto(baseURL)
    await expect(page.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    for (const width of [1280, 360, 390, 430]) await expectNoHorizontalOverflow(page, width)

    await page.getByLabel('Название инициативы').fill(mobileFeature)
    await page.getByLabel('Какого результата хотите достичь?').fill('Согласовать настоящую спецификацию и продолжить тот же run.')
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/runs')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(202)
    const { run_id: runID } = await createResponse.json()
    expect(runID).toBeTruthy()

    const readDetail = async () => {
      const runsResponse = await fetch(`${baseURL}/api/pipelines`)
      if (!runsResponse.ok) return null
      const runs = await runsResponse.json()
      const run = runs.find((value) => value.run_id === runID)
      if (!run) return null
      const response = await fetch(`${baseURL}/api/pipelines/${run.id}`)
      return response.ok ? response.json() : null
    }
    let detail
    await expect.poll(async () => {
      detail = await readDetail()
      return detail?.run?.status
    }, { timeout: 30_000 }).toMatch(/waiting_for_approval|failed/)
    const runEvidence = detail.run.status === 'failed'
      ? await readFile(path.join(target, '.ai-team', 'runs', runID, 'events.jsonl'), 'utf8').catch(() => '(no run event log)')
      : ''
    expect(detail.run.status, `run details: ${JSON.stringify(detail)}\nrun evidence:\n${runEvidence}\nweb server output:\n${webServer.outputText()}`).toBe('waiting_for_approval')

    // A real browser navigation away and return tears down/recreates the app
    // and its WebSocket while preserving same-tab sessionStorage. This is a
    // browser recovery check, not an iOS/Android background lifecycle check.
    await expect.poll(() => websocketUrls.length).toBeGreaterThan(0)
    const cursorBeforeReturn = await page.evaluate(() => JSON.parse(sessionStorage.getItem('ai-team:event-cursor') ?? '{"cursor":0}').cursor)
    expect(cursorBeforeReturn).toBeGreaterThan(0)
    const readsBeforeReturn = pipelineListReads
    const socketsBeforeReturn = websocketUrls.length
    await page.goto('about:blank')
    await page.goto(baseURL)
    await expect(page.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    await expect.poll(() => pipelineListReads).toBeGreaterThan(readsBeforeReturn)
    await expect.poll(() => websocketUrls.length).toBeGreaterThan(socketsBeforeReturn)
    expect(new URL(websocketUrls.at(-1)).searchParams.get('cursor')).toBe(String(cursorBeforeReturn))
    const returnedRunCard = page.locator('[class*="card"]').filter({ hasText: mobileFeature })
    await expect(returnedRunCard.getByText('ждёт решения', { exact: true })).toBeVisible()

    const runCard = page.locator('[class*="card"]').filter({ hasText: mobileFeature })
    await expect(runCard.getByText('ждёт решения', { exact: true })).toBeVisible({ timeout: 10_000 })
    for (const width of [360, 390, 430]) {
      await expectNoHorizontalOverflow(page, width)
      const feature = page.locator('span[class*="feature"]').filter({ hasText: mobileFeature })
      await expect(feature).toHaveCSS('overflow-wrap', 'anywhere')
      await expect.poll(() => feature.evaluate((element) => {
        const bounds = element.getBoundingClientRect()
        const featureStyle = getComputedStyle(element)
        const card = element.parentElement.parentElement
        const cardBounds = card.getBoundingClientRect()
        const cardStyle = getComputedStyle(card)
        const availableRight = cardBounds.right - Number.parseFloat(cardStyle.borderRightWidth) - Number.parseFloat(cardStyle.paddingRight)
        return bounds.height > Number.parseFloat(featureStyle.fontSize) * 1.5 && bounds.left >= cardBounds.left && bounds.right <= availableRight + 1
      })).toBe(true)
    }

    await runCard.click()
    await expect(page.getByRole('heading', { name: 'Человеческие решения' })).toBeVisible()
    for (const width of [1280, 360, 390, 430]) {
      await expectNoHorizontalOverflow(page, width)
      await expectQuestionContrast(page, width)
    }
    for (const width of [360, 390, 430]) {
      await page.setViewportSize({ width, height: 800 })
      await expectFingerTargets(page, [
        '[class*="back"]', '[class*="controlHeader"] button', '[class*="actions"] button',
        '[class*="questionText"] a', '[class*="edges"] a', 'summary',
      ])
    }
    detail = await readDetail()
    expect(detail.run.run_id).toBe(runID)
    expect(detail.run.status).toBe('waiting_for_approval')
    const pendingSpec = detail.approvals.find((approval) => approval.status === 'pending')
    expect(pendingSpec).toMatchObject({
      run_id: runID,
      from_stage: 'analyst',
      to_stage: 'architect',
      required_roles: ['product_owner'],
      payload: { kind: 'agreed_spec' },
    })
    await expect(page.getByText('Product Owner согласует требования перед архитектором')).toBeVisible()

    const specLink = page.getByRole('link', { name: 'Открыть спецификацию продукта' })
    await expect(specLink).toBeVisible()
    await specLink.click()
    await expect(page.getByRole('heading', { name: `Product spec for ${mobileFeature}` })).toBeVisible()
    for (const width of [1280, 360, 390, 430]) await expectNoHorizontalOverflow(page, width)
    for (const width of [360, 390, 430]) {
      await page.setViewportSize({ width, height: 800 })
      await expectFingerTargets(page, [
        '[class*="back"]', '[class*="toolbar"] button', '[class*="toggle"]',
        '[class*="editor"] button', '[class*="history"] button',
      ])
    }
    await page.getByRole('button', { name: '← Назад' }).click()
    await expect(page.getByRole('heading', { name: 'Человеческие решения' })).toBeVisible()

    const decisionResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === `/api/runs/${runID}/approvals/${pendingSpec.id}/decisions`)
    await page.getByRole('button', { name: 'Согласовать ТЗ и передать архитектору' }).click()
    expect((await decisionResponsePromise).status()).toBe(200)
    await expect.poll(async () => {
      detail = await readDetail()
      return detail?.approvals?.find((approval) => approval.id === pendingSpec.id)?.status
    }).toBe('resolved')
    expect(detail.run.run_id).toBe(runID)
    expect(detail.run.status).toBe('waiting_for_approval')

    await page.reload()
    await expect(page.getByText('ждёт решения', { exact: true })).toBeVisible()
    await expect(page.getByText('local-user (product_owner): approve_spec', { exact: true })).toBeVisible()
    detail = await readDetail()
    expect(detail.run.run_id).toBe(runID)
    expect(detail.approvals.find((approval) => approval.id === pendingSpec.id)?.status).toBe('resolved')

    const resumeResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === `/api/runs/${runID}/resume`)
    await page.getByRole('button', { name: 'Продолжить задачу' }).click()
    expect((await resumeResponsePromise).status()).toBe(202)
    const resumeDeadline = Date.now() + 30_000
    let resumedRunReady = false
    while (!resumedRunReady && Date.now() < resumeDeadline) {
      detail = await readDetail()
      const hasPendingArchitectApproval = detail?.approvals?.some((approval) =>
        approval.from_stage === 'architect' && approval.status === 'pending')
      if (detail?.run?.status === 'failed' && !hasPendingArchitectApproval) {
        const events = await readFile(path.join(target, '.ai-team', 'runs', runID, 'events.jsonl'), 'utf8').catch(() => '(no run event log)')
        throw new Error(`resumed run failed before recording the architect approval\nrun evidence:\n${events}\nweb server output:\n${webServer.outputText()}`)
      }
      resumedRunReady = detail?.run?.run_id === runID && detail.run.status === 'failed' && hasPendingArchitectApproval
      if (!resumedRunReady) await new Promise((resolve) => setTimeout(resolve, 250))
    }
    expect(resumedRunReady, `resumed run did not persist its terminal status and architect approval: ${JSON.stringify(detail)}`).toBe(true)
    expect(detail.run.run_id).toBe(runID)
    expect(detail.run.status).toBe('failed')
    expect(webServer.outputText()).toContain('нет успешно выполненного required check класса unit/integration/e2e')

    // A full reload proves the decision and resumed lifecycle are served from
    // persisted run/approval state, not from browser memory.
    await page.reload()
    await expect(page.locator('[class*="meta"] > span[class*="failed"]')).toHaveText('ошибка')
    detail = await readDetail()
    expect(detail.run.run_id).toBe(runID)
    expect(detail.run.status).toBe('failed')
    expect(detail.approvals.find((approval) => approval.id === pendingSpec.id)?.status).toBe('resolved')
    expect(detail.approvals.some((approval) => approval.from_stage === 'architect' && approval.status === 'pending')).toBe(true)
    const lifecycle = JSON.parse(await readFile(path.join(target, '.ai-team', 'state', 'runs', `${runID}.json`), 'utf8'))
    expect(lifecycle.run_id).toBe(runID)
    expect(lifecycle.phase).toBe('terminal')

    // Force an actual Chromium network loss and restoration. The browser must
    // reconnect its real WebSocket with the session's durable event cursor.
    await expect.poll(() => websocketUrls.length).toBeGreaterThan(0)
    const previousSockets = websocketUrls.length
    const cursor = await page.evaluate(() => JSON.parse(sessionStorage.getItem('ai-team:event-cursor') ?? '{"cursor":0}').cursor)
    expect(cursor).toBeGreaterThan(0)
    await page.context().setOffline(true)
    expect(await page.evaluate(async () => {
      try {
        await fetch(`/api/pipelines?offline-probe=${Date.now()}`, { cache: 'no-store' })
        return false
      } catch {
        return true
      }
    })).toBe(true)
    // Restart the real server while Chromium is offline. This closes the
    // established TCP/WebSocket connection and forces the app hook to recover
    // against the same persisted server state after connectivity returns.
    await stop(webServer)
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist')], {
      cwd: target,
      env: serverEnv,
    })
    await waitForServer(baseURL, webServer)
    await page.context().setOffline(false)
    await expect.poll(() => websocketUrls.length).toBeGreaterThan(previousSockets)
    expect(new URL(websocketUrls.at(-1)).searchParams.get('cursor')).toBe(String(cursor))

    // Create a separate probe run through the actual UI after reconnection.
    // Seeing its persisted event arrive on the app's WebSocket proves the new
    // connection completed and is delivering live server events.
    await page.goto(baseURL)
    await page.getByLabel('Название инициативы').fill('websocket-recovery-probe')
    await page.getByLabel('Какого результата хотите достичь?').fill('Проверить доставку события после восстановления WebSocket.')
    const recoveryRunResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/runs')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const recoveryRunResponse = await recoveryRunResponsePromise
    expect(recoveryRunResponse.status()).toBe(202)
    const { run_id: recoveryRunID } = await recoveryRunResponse.json()
    await expect.poll(() => websocketFrames.some(({ event }) => event.run_id === recoveryRunID)).toBe(true)
    const recoveryFrame = websocketFrames.find(({ event }) => event.run_id === recoveryRunID)
    expect(new URL(recoveryFrame.url).searchParams.get('cursor')).toBe(String(cursor))

    const api = (method, path) => apiResponses.some((response) => response.method === method && response.path === path && response.status >= 200 && response.status < 300)
    expect(api('GET', `/api/pipelines/${detail.run.id}`)).toBe(true)
    expect(api('GET', `/api/pipelines/${detail.run.id}/artifacts`)).toBe(true)
    expect(apiResponses.some((response) => response.method === 'GET' && response.path.startsWith(`/api/runs/${runID}/artifacts/`) && response.status === 200)).toBe(true)
    expect(apiResponses.some((response) => response.method === 'POST' && response.path.startsWith(`/api/runs/${runID}/approvals/`) && response.path.endsWith('/decisions') && response.status === 200)).toBe(true)
    expect(api('POST', `/api/runs/${runID}/resume`)).toBe(true)
    // This is Chromium viewport/tab/network simulation. No physical iOS or
    // Android browser/OS suspension and return lifecycle is exercised here.
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
