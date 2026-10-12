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

test('tasks board creates a task from the browser and shows it by stage', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-tasks-board-e2e-'))
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

    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${baseURL}/tasks`)
    const localToken = (await readFile(path.join(target, '.ai-team', 'web.token'), 'utf8')).trim()
    await page.getByLabel('Токен доступа').fill(localToken)
    await page.getByRole('button', { name: 'Войти' }).click()

    // Board with template-stage columns and Russian title/lang contract.
    await expect(page.getByRole('heading', { name: 'Задачи', level: 1 })).toBeVisible()
    await expect(page).toHaveTitle('Задачи — ai-team')
    await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
    await expect(page.getByRole('region', { name: 'Доска по этапам' })).toBeVisible()
    await expect(page.getByLabel('Этап: Бизнес-идея')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391)

    // Create a task from the dedicated screen with template, mode and idea.
    await page.getByRole('link', { name: 'Новая задача' }).click()
    await expect(page.getByRole('heading', { name: 'Новая задача' })).toBeVisible()
    await page.getByLabel('Название').fill('browser-board-task')
    await page.getByLabel('Текст идеи').fill('Создать задачу из браузерной формы.')
    await page.getByLabel('Внешние ссылки').fill('JIRA-77 https://tracker.example/JIRA-77')
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/runs')
    await page.getByRole('button', { name: 'Создать задачу' }).click()
    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(202)
    const { run_id: runID } = await createResponse.json()
    expect(runID).toBeTruthy()
    await expect(page.getByText(`Задача создана: ${runID}`)).toBeVisible()

    // The new task appears on the board through the live event stream.
    const listResponse = await page.request.get(`${baseURL}/api/runs?limit=10`)
    expect(listResponse.ok()).toBe(true)
    const list = await listResponse.json()
    const created = list.runs.find((value) => value.run_id === runID)
    expect(created).toBeTruthy()
    expect(created.feature).toBe('browser-board-task')
    expect(created.external_links).toEqual(['JIRA-77 https://tracker.example/JIRA-77'])

    await page.getByRole('link', { name: 'Доска' }).click()
    const card = page.locator('article[aria-label="Задача browser-board-task"]')
    await expect(card).toBeVisible({ timeout: 10_000 })
    await expect(card.getByText('JIRA-77 https://tracker.example/JIRA-77')).toBeVisible()
    // Short hash with copy control, per §11.
    await expect(card.getByRole('button', { name: 'Скопировать ID задачи' })).toBeVisible()

    // Filters narrow the list through the task API.
    await page.getByRole('button', { name: 'Список' }).click()
    await expect(page.getByLabel('Список задач')).toBeVisible()
    await page.getByLabel('Режим').selectOption('auto')
    await expect.poll(async () => {
      const response = await page.request.get(`${baseURL}/api/runs?mode=auto&limit=50`)
      const body = await response.json()
      return body.runs.every((value) => value.mode === 'auto')
    }).toBe(true)
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
