import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
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

test('observability page shows an empty period and blocks CSV export for it', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-observability-e2e-'))
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

    await page.goto(`${baseURL}/observability`)
    await expect(page).toHaveURL(/\/observability$/)
    await expect(page.getByRole('heading', { name: 'Наблюдаемость' })).toBeVisible()

    await page.getByLabel('С', { exact: true }).fill('2020-01-01')
    await page.getByLabel('По', { exact: true }).fill('2020-01-31')
    const metricsResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/api/metrics/flow' && url.searchParams.get('from') === '2020-01-01' && url.searchParams.get('to') === '2020-02-01'
    })
    await page.getByRole('button', { name: 'Применить' }).click()
    expect((await metricsResponse).status()).toBe(200)
    await expect(page.getByText('За выбранный период задач и попыток этапов нет.')).toBeVisible()
    await expect(page.getByText('Показан период: 01.01.2020 — 31.01.2020 (UTC)')).toBeVisible()
    await expect(page.getByLabel('С', { exact: true })).toHaveAccessibleDescription('Даты в UTC, обе границы включительно.')
    // Nothing to export for an empty period.
    await expect(page.getByRole('button', { name: 'Экспорт CSV' })).toBeDisabled()
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})

// Non-empty data from a real run: the e2etest build drives the analyst stage
// with mock-opencode, so the page and its CSV show a recorded stage attempt.
test('observability page shows a recorded stage attempt and exports it as CSV', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-observability-data-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const mockBin = path.join(tempRoot, 'mockbin')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  let webServer

  try {
    run('go', ['build', '-tags=e2etest', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    await mkdir(target, { recursive: true })
    run('git', ['init', '-b', 'main'], { cwd: target })
    run('git', ['config', 'user.name', 'AI Team Observability E2E'], { cwd: target })
    run('git', ['config', 'user.email', 'ai-team-observability-e2e@example.test'], { cwd: target })
    run('git', ['remote', 'add', 'origin', 'https://example.test/ai-team/observability-fixture.git'], { cwd: target })
    await writeFile(path.join(target, 'README.md'), 'Observability E2E fixture.\n')
    run('git', ['add', 'README.md'], { cwd: target })
    run('git', ['commit', '-m', 'Initialize observability E2E target'], { cwd: target })
    run(binary, ['init', '--target', target], { cwd: repoDir })

    await mkdir(mockBin, { recursive: true })
    await symlink(path.join(repoDir, 'e2etest', 'mock-opencode.sh'), path.join(mockBin, 'opencode'))
    await writeFile(path.join(mockBin, 'gh'), '#!/bin/sh\nif [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo authenticated; exit 0; fi\nexit 1\n', { mode: 0o755 })
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist')], {
      cwd: target,
      env: { ...process.env, PATH: `${mockBin}${path.delimiter}${process.env.PATH ?? ''}`, AI_TEAM_E2E_IN_MEMORY_LEGACY_RUNTIME: '1' },
    })
    await waitForServer(baseURL, webServer)

    await page.goto(`${baseURL}/tasks`)
    const localToken = (await readFile(path.join(target, '.ai-team', 'web.token'), 'utf8')).trim()
    await page.getByLabel('Токен доступа').fill(localToken)
    await page.getByRole('button', { name: 'Войти' }).click()

    await page.getByRole('link', { name: 'Новая задача' }).click()
    await page.getByLabel('Название').fill('observability-data')
    await page.getByLabel('Текст идеи').fill('Создать задачу, чтобы на странице наблюдаемости были данные.')
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/runs')
    await page.getByRole('button', { name: 'Создать задачу' }).click()
    expect((await createResponsePromise).status()).toBe(202)

    // Wait until the server has recorded at least one stage attempt.
    let flow
    await expect.poll(async () => {
      const response = await page.request.get(`${baseURL}/api/metrics/flow`)
      flow = response.ok() ? await response.json() : null
      return flow?.by_stage?.length ?? 0
    }, { timeout: 60_000, message: `web server output:\n${webServer.outputText()}` }).toBeGreaterThan(0)
    const stage = flow.by_stage[0].stage

    await page.goto(`${baseURL}/observability`)
    await expect(page.getByRole('heading', { name: 'Разложение по этапам' })).toBeVisible()
    await expect(page.getByText('За выбранный период задач и попыток этапов нет.')).toHaveCount(0)
    const stageTable = page.getByRole('heading', { name: 'Разложение по этапам' }).locator('xpath=ancestor::section[1]')
    await expect(stageTable.getByRole('rowheader', { name: stage })).toBeVisible()
    await expect(page.getByText(/^Показан период: \d{2}\.\d{2}\.\d{4} — \d{2}\.\d{2}\.\d{4} \(UTC\)$/)).toBeVisible()

    const exportButton = page.getByRole('button', { name: 'Экспорт CSV' })
    await expect(exportButton).toBeEnabled()
    const downloadPromise = page.waitForEvent('download')
    await exportButton.click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toBe('flow-metrics.csv')
    const csv = await readFile(await download.path(), 'utf8')
    expect(csv.split('\n')[0]).toBe('metric,stage,value,unit,attestation,source,basis')
    expect(csv).toMatch(new RegExp(`^stage_attempts,"${stage}",[1-9]\\d*,`, 'm'))
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
