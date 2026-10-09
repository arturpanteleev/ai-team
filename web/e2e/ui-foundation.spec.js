import { spawn, spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
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

async function expectThemeContrast(page) {
  const violations = await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement)
    const color = (name) => style.getPropertyValue(name).trim()
    const luminance = (hex) => {
      const values = hex.replace('#', '').match(/.{2}/g).map((part) => parseInt(part, 16) / 255)
      const linear = values.map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
      return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722
    }
    const ratio = (foreground, background) => {
      const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a)
      return (values[0] + 0.05) / (values[1] + 0.05)
    }
    const pairs = [
      ['--text-primary', '--canvas'],
      ['--text-primary', '--surface-muted'],
      ['--text-secondary', '--surface'],
      ['--text-secondary', '--surface-muted'],
      ['--text-muted', '--canvas'],
      ['--link', '--canvas'],
      ['--accent-contrast', '--accent'],
      ['--success-fg', '--success-bg'],
      ['--warning-fg', '--warning-bg'],
      ['--danger-fg', '--danger-bg'],
      ['--info-fg', '--info-bg'],
    ]
    return pairs.map(([foreground, background]) => ({
      pair: `${foreground} on ${background}`,
      ratio: ratio(color(foreground), color(background)),
    })).filter((pair) => pair.ratio < 4.5)
  })
  expect(violations).toEqual([])
}

test('Russian UI supports both accessible themes and fits a 390px viewport', async ({ page }) => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'ai-team-ui-foundation-e2e-'))
  const target = path.join(tempRoot, 'target')
  const binary = path.join(tempRoot, 'ai-team')
  const port = await unusedPort()
  const baseURL = `http://127.0.0.1:${port}`
  let webServer

  try {
    run('go', ['build', '-o', binary, './cmd/ai-team'], { cwd: repoDir })
    await mkdir(target, { recursive: true })
    run(binary, ['init', '--target', target], { cwd: repoDir })
    webServer = start(binary, ['web', '--target', target, '--port', String(port), '--dist', path.join(webDir, 'dist')], { cwd: target })
    await waitForServer(baseURL, webServer)

    await page.goto(baseURL)
    await expect(page.getByRole('heading', { name: 'Задачи' })).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
    await expect(page).toHaveTitle('Задачи — ai-team')
    await expectThemeContrast(page)

    const themeToggle = page.getByRole('button', { name: 'Переключить тему' })
    await themeToggle.click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expectThemeContrast(page)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await themeToggle.click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')

    await page.setViewportSize({ width: 390, height: 844 })
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391)
    const filterToggle = page.getByRole('button', { name: 'Статус: Все · Фильтры' })
    await expect(filterToggle).toBeVisible()
    await filterToggle.click()
    await expect(filterToggle).toHaveAttribute('aria-expanded', 'true')
    const filters = page.locator('#pipeline-status-filters')
    await expect(filters).toBeVisible()
    const filterWidth = await filters.evaluate((element) => ({ scroll: element.scrollWidth, client: element.clientWidth }))
    expect(filterWidth.scroll).toBeLessThanOrEqual(filterWidth.client)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391)
  } finally {
    await stop(webServer)
    await rm(tempRoot, { recursive: true, force: true })
  }
})
