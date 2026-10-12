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

test('observability page shows an empty period and exports CSV for it', async ({ page }) => {
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

    await page.getByRole('link', { name: 'Наблюдаемость' }).click()
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

    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Экспорт CSV' }).click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toBe('flow-metrics.csv')
    const csv = await readFile(await download.path(), 'utf8')
    expect(csv.split('\n')[0]).toBe('metric,stage,value,unit,attestation,source,basis')
    expect(csv).toContain('lead_time_count,"",0,')
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
