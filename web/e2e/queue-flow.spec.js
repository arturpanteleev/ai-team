import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from '@playwright/test'

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

test('real React dashboard shows durable scheduler queue, worker failure, reload, and queued cancel', async ({ page }) => {
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
    await page.goto(baseURL)
    await expect(page.getByRole('heading', { name: 'Pipeline Runs' })).toBeVisible()
    await page.getByLabel('Название инициативы').fill('browser-queued-work')
    await page.getByLabel('Какого результата хотите достичь?').fill('Wait for a real scheduler worker')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()

    const firstRun = page.locator('[class*="card"]').filter({ hasText: 'browser-queued-work' })
    await expect(firstRun.getByText('queued', { exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(firstRun.getByText(/Queue #\d+/)).toBeVisible()

    // The wrapper pauses after the queue claim, making the running state
    // observable in the real UI before the worker's deliberately empty PATH
    // makes runtime preflight fail deterministically.
    const wrapper = path.join(tempRoot, 'worker-wrapper.sh')
    await writeFile(wrapper, `#!/bin/sh\n/bin/sleep 2\nexec '${binary}' "$@"\n`, { mode: 0o755 })
    worker = start(binary, ['scheduler-worker', '--target', target, '--scheduler-db', '.ai-team/scheduler.db', '--worker-command', wrapper, '--worker-id', 'browser-e2e-worker', '--once', '--poll-interval', '50ms'], {
      cwd: target,
      env: { ...process.env, PATH: tempRoot },
    })

    await expect(firstRun.getByText('running', { exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(firstRun.getByText('failed', { exact: true })).toBeVisible({ timeout: 20_000 })
    await expect(firstRun.getByRole('alert')).toContainText(/preflight|opencode/i)
    await page.reload()
    const recoveredFirstRun = page.locator('[class*="card"]').filter({ hasText: 'browser-queued-work' })
    await expect(recoveredFirstRun.getByText('failed', { exact: true })).toBeVisible()
    await expect(recoveredFirstRun.getByRole('alert')).toContainText(/preflight|opencode/i)

    await page.getByLabel('Название инициативы').fill('browser-cancel-after-reload')
    await page.getByLabel('Какого результата хотите достичь?').fill('Remain queued until cancellation')
    await page.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const queuedRun = page.locator('[class*="card"]').filter({ hasText: 'browser-cancel-after-reload' })
    await expect(queuedRun.getByText('queued', { exact: true })).toBeVisible()
    await expect(queuedRun.getByText(/Queue #\d+/)).toBeVisible()

    await page.reload()
    const recoveredQueuedRun = page.locator('[class*="card"]').filter({ hasText: 'browser-cancel-after-reload' })
    await expect(recoveredQueuedRun.getByText('queued', { exact: true })).toBeVisible()
    await recoveredQueuedRun.getByRole('button', { name: 'Отменить' }).click()
    await expect(recoveredQueuedRun.getByText('canceled', { exact: true })).toBeVisible()
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
    await first.getByLabel('Access token').fill(token)
    await first.getByRole('button', { name: 'Войти' }).click()
    await expect(first.getByRole('heading', { name: 'Pipeline Runs' })).toBeVisible()

    // A new JS context has no in-memory CSRF token; the HttpOnly cookie must
    // recover it before the command is sent.
    await first.reload()
    await expect(first.getByRole('heading', { name: 'Pipeline Runs' })).toBeVisible()
    const second = await context.newPage()
    await second.goto(baseURL)
    await expect(second.getByRole('heading', { name: 'Pipeline Runs' })).toBeVisible()

    // Team read APIs use the authenticated same-origin session without a
    // write CSRF header. The owner can load members/audit and the UI refuses
    // an empty role edit before sending a PATCH.
    await second.getByRole('link', { name: 'Team' }).click()
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

    await second.getByRole('link', { name: 'Pipelines' }).click()
    await expect(second.getByRole('heading', { name: 'Pipeline Runs' })).toBeVisible()
    await second.getByLabel('Название инициативы').fill('authenticated-after-reconnect')
    await second.getByLabel('Какого результата хотите достичь?').fill('Submit with a recovered CSRF token')
    await second.getByRole('button', { name: 'Создать инициативу и передать аналитику' }).click()
    const runCard = second.locator('[class*="card"]').filter({ hasText: 'authenticated-after-reconnect' })
    await expect(runCard.getByText('queued', { exact: true })).toBeVisible({ timeout: 10_000 })

    // Browser sessions are intentionally process-local. Restarting the server
    // revokes that session while retaining the browser's HttpOnly cookie.
    await stop(webServer)
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist'), '--scheduler-db', '.ai-team/scheduler.db'], {
      cwd: target,
      env: { ...process.env, AI_TEAM_AUTH_SECRET: secret },
    })
    await waitForServer(baseURL, webServer)
    await second.reload()
    await expect(second.getByLabel('Access token')).toBeVisible()
    await expect(second.getByText(/Сессия истекла или отсутствует/)).toBeVisible()
    await context.close()
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
