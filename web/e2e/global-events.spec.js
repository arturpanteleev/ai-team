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
    const events = [
      // Keep fixtures beyond the live events generated while creating the task.
      { version: 1, cursor: 100, run_id: runID, sequence: 1, type: 'run_started', timestamp: at, data: { feature: 'browser-event-feed' } },
      { version: 1, cursor: 101, run_id: runID, sequence: 2, type: 'check_error', stage: 'qa', severity: 'warning', timestamp: at, data: { feature: 'browser-event-feed', reason: 'Проверка требует внимания' } },
    ]
    await page.route('**/*', async (route) => {
      const url = new URL(route.request().url())
      if (url.pathname !== '/api/events') return route.continue()
      const warningsOnly = url.searchParams.get('warnings') === 'true'
      await route.fulfill({ json: { events: warningsOnly ? [events[1]] : events, next_cursor: 101 } })
    })

    await page.getByRole('link', { name: 'Лента' }).click()
    await expect(page.getByRole('heading', { name: 'Лента событий' })).toBeVisible()
    await expect(page.getByText('Задача создана')).toBeVisible()
    await expect(page.getByText('Ошибка проверки')).toBeVisible()

    const warningResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/events' && new URL(response.url()).searchParams.get('warnings') === 'true')
    await page.getByRole('button', { name: 'Предупреждения' }).click()
    await warningResponse
    await expect(page.getByText('Задача создана')).toHaveCount(0)
    await expect(page.getByText('Проверка требует внимания')).toBeVisible()

    await page.getByRole('link', { name: 'Задача: browser-event-feed' }).click()
    await expect(page).toHaveURL(/\/pipelines\/\d+$/)
    await expect(page.getByRole('heading', { name: 'browser-event-feed' })).toBeVisible()
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
