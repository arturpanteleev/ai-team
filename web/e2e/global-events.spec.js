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

test('global event feed filters warnings and opens the linked task', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-global-events-e2e-'))
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

    await page.getByRole('link', { name: 'Новая задача' }).click()
    await page.getByLabel('Название').fill('browser-event-feed')
    await page.getByLabel('Текст идеи').fill('Создать задачу для проверки глобальной ленты событий.')
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/runs')
    await page.getByRole('button', { name: 'Создать задачу' }).click()
    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(202)
    const { run_id: runID } = await createResponse.json()

    const at = new Date().toISOString()
    // Fixture cursors are beyond the live events generated while creating the
    // task. The server pages newest-first, so the mock serves pages in that
    // order and asserts the parameters the feed sends.
    const startedEvent = { version: 1, cursor: 100, run_id: runID, sequence: 1, type: 'run_started', warning: false, timestamp: at, data: { feature: 'browser-event-feed', reason: 'fixture-oldest' } }
    const warningEvent = { version: 1, cursor: 101, run_id: runID, sequence: 2, type: 'check_error', stage: 'qa', severity: 'warning', warning: true, timestamp: at, data: { feature: 'browser-event-feed', reason: 'Проверка требует внимания' } }
    const newestEvent = { version: 1, cursor: 102, run_id: runID, sequence: 3, type: 'attempt_started', stage: 'idea', warning: false, timestamp: at, data: { feature: 'browser-event-feed', reason: 'fixture-newest' } }
    const feedRequests = []
    await page.route('**/*', async (route) => {
      const url = new URL(route.request().url())
      if (url.pathname !== '/api/events') return route.continue()
      const params = Object.fromEntries(url.searchParams)
      feedRequests.push(params)
      if (params.order !== 'desc' || 'after' in params || params.before === '0') {
        return route.fulfill({ status: 400, json: { error: `unexpected feed query ${url.search}`, code: 'bad_event_query' } })
      }
      if (params.warnings === 'true') return route.fulfill({ json: { events: [warningEvent], next_cursor: 101, has_more: false } })
      if (!('before' in params)) return route.fulfill({ json: { events: [newestEvent, warningEvent], next_cursor: 101, has_more: true } })
      if (params.before === '101') return route.fulfill({ json: { events: [startedEvent], next_cursor: 100, has_more: false } })
      return route.fulfill({ status: 400, json: { error: `unexpected before ${params.before}`, code: 'bad_event_query' } })
    })

    await page.getByRole('link', { name: 'Лента' }).click()
    await expect(page.getByRole('heading', { name: 'Лента событий' })).toBeVisible()
    await expect(page.getByText('Ошибка проверки')).toBeVisible()
    expect(feedRequests[0]).toEqual({ order: 'desc', limit: '100' })
    // Live WebSocket events may also be listed; only the fixtures' relative
    // order is asserted: newest first, the older page appended below.
    const fixtureOrder = () => page.getByRole('list', { name: 'Список событий' }).locator('article').evaluateAll((articles) =>
      articles.map((article) => article.textContent ?? '')
        .map((text) => ['fixture-newest', 'Проверка требует внимания', 'fixture-oldest'].find((marker) => text.includes(marker)))
        .filter(Boolean))
    await expect.poll(fixtureOrder).toEqual(['fixture-newest', 'Проверка требует внимания'])

    await page.getByRole('button', { name: 'Показать ещё' }).click()
    await expect.poll(fixtureOrder).toEqual(['fixture-newest', 'Проверка требует внимания', 'fixture-oldest'])
    expect(feedRequests.at(-1)).toEqual({ order: 'desc', limit: '100', before: '101' })
    await expect(page.getByRole('button', { name: 'Показать ещё' })).toHaveCount(0)

    const warningResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/events' && new URL(response.url()).searchParams.get('warnings') === 'true')
    await page.getByRole('button', { name: 'Предупреждения' }).click()
    await warningResponse
    await expect(page.getByText('fixture-oldest')).toHaveCount(0)
    await expect(page.getByText('Проверка требует внимания')).toBeVisible()

    await page.getByRole('link', { name: 'Задача: browser-event-feed' }).click()
    await expect(page).toHaveURL(/\/pipelines\/\d+$/)
    await expect(page.getByRole('heading', { name: 'browser-event-feed' })).toBeVisible()
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
