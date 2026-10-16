#!/usr/bin/env node
// run-ui.mjs — сквозная демонстрация S17 через настоящий Web UI (Playwright,
// Chromium) поверх настоящего `ai-team web` в локальном режиме.
//
//   node scripts/demo/run-ui.mjs --home <DEMO_HOME> --port <PORT> [--screens <dir>]
//
// Окружение готовит scripts/demo/setup.sh, сервер запускает run-demo.sh. Все
// действия человека выполняются кнопками и формами страницы; REST используется
// только для чтения — чтобы дождаться, пока контроллер закончит работу, и
// проверить итоги. Скриншоты ключевых шагов кладутся в docs/demo/screens/.
import { createRequire } from 'node:module'
import { appendFileSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { Checklist, atStage, describeAttempts, finished, gateAttempts, hasPending, openApi, settle, sleep } from './lib/flow.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(here, '../..')
const args = Object.fromEntries(process.argv.slice(2).reduce((acc, value, index, all) => (value.startsWith('--') ? [...acc, [value.slice(2), all[index + 1]]] : acc), []))
const home = path.resolve(args.home ?? process.env.DEMO_HOME ?? '')
const port = args.port ?? process.env.DEMO_PORT ?? '18080'
const phase = args.phase ?? 'all' // main | loops | all
const screens = path.resolve(args.screens ?? path.join(repoRoot, 'docs/demo/screens'))
if (!args.home && !process.env.DEMO_HOME) throw new Error('укажите --home <DEMO_HOME>')

const require = createRequire(path.join(repoRoot, 'web/package.json'))
const { chromium } = require('@playwright/test')

const { token, baseURL, api } = openApi(home, port)
await api.login()
const checks = new Checklist()
mkdirSync(screens, { recursive: true })
if (phase !== 'loops') for (const file of readdirSync(screens)) if (/\.(png|jpg)$/.test(file)) rmSync(path.join(screens, file))

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
page.setDefaultTimeout(30000)
let shotNo = readdirSync(screens).filter((file) => /\.(png|jpg)$/.test(file)).length
const shot = async (name) => {
  shotNo += 1
  await page.waitForTimeout(500)
  const file = path.join(screens, `${String(shotNo).padStart(2, '0')}-${name}.jpg`)
  try {
    await page.screenshot({ path: file, fullPage: !/^(journal|events)/.test(name), type: 'jpeg', quality: 70, timeout: 60000, animations: 'disabled' })
  } catch {
    // Полная страница иногда не снимается (шрифты, перерисовка): снимаем видимую область.
    await page.screenshot({ path: file, type: 'jpeg', quality: 70, timeout: 60000, animations: 'disabled' })
  }
}
const log = (text) => console.log(`\n▶ ${text}`)
const button = (name, exact = true) => page.getByRole('button', { name, exact })
const click = async (name, exact = true) => { await button(name, exact).first().click() }
const wait = (predicate, timeout) => settle(api, runId, predicate, timeout)

let runId = ''
const demoDir = path.join(home, 'repo', '.demo')
mkdirSync(demoDir, { recursive: true })
const writeMode = (file, text) => writeFileSync(path.join(home, 'state', file), text)
if (phase !== 'loops') {
  writeMode('coder-mode', 'clean')
  writeMode('coder-calls', '0')
  writeMode('llm-mode', 'pass')
}

async function openTask(tab) {
  await page.goto(`${baseURL}/tasks/${runId}`)
  await page.getByRole('tab', { name: 'Результат' }).waitFor()
  if (tab) await page.getByRole('tab', { name: tab }).click()
}

async function submitMarkdown(markdown, note) {
  await openTask()
  await click('Сдать результат')
  await page.getByLabel('Результат в Markdown').fill(markdown)
  await page.getByLabel('Что сделано').fill(note)
  await click('Сдать результат')
}

// Принять переход. Если подтверждение записалось, а задача не пошла дальше
// (например, процесс контроллера прерывали), страница предлагает «Продолжить
// задачу»: сценарий нажимает её и отмечает это в выводе.
async function acceptTransition() {
  await sleep(1500)
  await openTask()
  const before = (await api.must('GET', `/runs/${runId}`)).approvals.filter((a) => a.status === 'pending').map((a) => a.id).join()
  await button(/^Принять и (перейти|завершить)/).first().click()
  for (let i = 0; i < 40; i += 1) {
    await sleep(500)
    const { detail, pending } = await (await import('./lib/flow.mjs')).snapshot(api, runId)
    if (detail.state?.phase === 'terminal' || detail.run.status === 'running' || pending.map((a) => a.id).join() !== before) return
  }
  await openTask()
  const resume = button('Продолжить задачу')
  if (await resume.count()) {
    console.log('  ⚠ возобновление после подтверждения не произошло само — нажимаем «Продолжить задачу»')
    resumeNudges += 1
    await resume.first().click()
  }
}
let resumeNudges = 0

async function newVersionAfterFailure() {
  await openTask()
  await click('Отправить новую версию')
}

// ───────────────────────────────────────── вход, проект, задача
log('Вход по локальному токену')
await page.goto(baseURL)
await page.getByLabel('Токен доступа').fill(token)
await page.getByRole('button', { name: 'Войти' }).click()
await page.getByRole('heading', { name: 'Очередь' }).waitFor()

async function mainFlow() {
log('Проект — папка для задач')
await page.goto(`${baseURL}/projects`)
await page.getByLabel('Название').fill('Демо S17')
await page.getByLabel('Описание').fill('Сквозная демонстрация: гейты, исполнители, трекер и доставка')
await click('Создать проект')
await page.getByText('Демо S17').first().waitFor()
await shot('project')

log('Задача по шаблону idea-to-prod (9 этапов)')
await page.goto(`${baseURL}/tasks/new`)
await page.selectOption('#task-project', { label: 'Демо S17' })
await page.getByLabel('Название').fill('Экспорт отчёта в CSV')
await page.getByLabel('Идентификатор').fill('csv-export')
await page.getByLabel('Текст идеи').fill('Нужно выгружать отчёт по форматам в CSV, чтобы аналитики могли открыть его в таблице.')
await shot('new-task')
await click('Создать задачу')
await page.getByRole('link', { name: 'Открыть задачу' }).click()
  await page.waitForURL(/\/tasks\/\d{8}T/)
runId = new URL(page.url()).pathname.split('/').pop()
console.log('  задача', runId)
await wait(atStage('intent', 'results'))
const stagesInTemplate = (await api.must('GET', `/runs/${runId}`)).template.stages
checks.check('шаблон задачи — 9 этапов idea-to-prod', stagesInTemplate.length === 9, stagesInTemplate.map((s) => s.id).join(' → '))

log('Фейковый трекер: связь 1:1 с карточкой DEMO-1')
await openTask()
await page.getByLabel('Ключ карточки').fill('DEMO-1')
await click('Связать с карточкой')
await page.getByText('DEMO-1').first().waitFor()
await shot('task-linked-to-tracker')

// ───────────────────────────────────────── intent
log('Этап 1. Бизнес-идея: подтверждает владелец (bo)')
await openTask()
await click('Принять')
await wait(atStage('product_spec', 'results'))

// ───────────────────────────────────────── product_spec
log('Этап 2. Спецификация без «Критериев приёмки»: кодовый гейт даёт fail')
await submitMarkdown('# Экспорт CSV\n\nНужно выгружать отчёт в CSV.\n', 'Первая версия')
await wait(hasPending('check_decision', 'stage_check'))
await openTask('Гейты')
await page.getByText('required_sections').first().waitFor()
await shot('spec-code-gate-fail')
let rows = await gateAttempts(api, runId, 'product_spec')
checks.check('required_sections: execution=succeeded, verdict=fail', rows.some((a) => a.gate_name === 'required_sections' && a.execution === 'succeeded' && a.verdict === 'fail'), describeAttempts(rows))
checks.check('следующие группы (LLM, человек) не стартовали после fail', !rows.some((a) => a.gate_name === 'spec_llm_review' || a.gate_name === 'po_approval'))

log('Новая версия: критерии есть, метрики успеха нет (рекомендация), LLM-гейт, решение PO')
await newVersionAfterFailure()
await wait(hasPending('input', 'human_input'))
await submitMarkdown('# Экспорт CSV\n\nНужно выгружать отчёт в CSV.\n\n## Критерии приёмки\n\n- Файл открывается в Excel без ошибок кодировки.\n- Пустой отчёт даёт файл только с заголовком.\n', 'Добавлены критерии приёмки')
await wait(hasPending('approve', 'graph_outcome:passed'))
await openTask('Гейты')
await page.getByText('spec_llm_review').first().waitFor()
await shot('spec-advisory-llm-human')
rows = await gateAttempts(api, runId, 'product_spec')
const v2 = rows.filter((a) => a.sha !== rows[0].sha)
checks.check('новая версия запустила все группы заново (новый хеш)', v2.some((a) => a.gate_name === 'required_sections' && a.verdict === 'pass'), describeAttempts(v2))
checks.check('рекомендательный success_metric: fail не блокирует (warning)', v2.some((a) => a.gate_name === 'success_metric' && a.mode === 'advisory' && a.verdict === 'fail'))
checks.check('LLM-гейт (мок рантайма) вернул pass', v2.some((a) => a.gate_name === 'spec_llm_review' && a.verdict === 'pass'))
checks.check('решение человека (PO) после автоматических групп', v2.some((a) => a.gate_name === 'po_approval'))

log('Решение PO: принять спецификацию')
await acceptTransition()
await wait(atStage('tech_design', 'results'))

// ───────────────────────────────────────── tech_design
log('Этап 3. Техническая архитектура: гейт внешней программы на хосте (TODO → fail)')
await submitMarkdown('# Архитектура\n\nTODO описать формат CSV и кодировку.\n', 'Черновик')
await wait(hasPending('check_decision', 'stage_check'))
await openTask('Гейты')
await page.getByText('host_todo_check').first().waitFor()
await shot('tech-host-program-gate-fail')
rows = await gateAttempts(api, runId, 'tech_design')
checks.check('host_todo_check: исполнитель external, verdict=fail', rows.some((a) => a.gate_name === 'host_todo_check' && a.executor === 'external' && a.verdict === 'fail'), describeAttempts(rows))

log('Новая версия без TODO, но с FIXME: хост пройден, Docker-гейт даёт fail')
await newVersionAfterFailure()
await wait(hasPending('input', 'human_input'))
await submitMarkdown('# Архитектура\n\nFIXME уточнить кодировку.\n', 'Убрал TODO')
await wait(hasPending('check_decision', 'stage_check'))
await openTask('Гейты')
await page.getByText('docker_fixme_check').first().waitFor()
await shot('tech-docker-gate-fail')
rows = await gateAttempts(api, runId, 'tech_design')
checks.check('docker_fixme_check: исполнитель docker, verdict=fail', rows.some((a) => a.gate_name === 'docker_fixme_check' && a.executor === 'docker' && a.verdict === 'fail'), describeAttempts(rows))

log('Третья версия: оба гейта проходят')
await newVersionAfterFailure()
await wait(hasPending('input', 'human_input'))
await submitMarkdown('# Архитектура\n\nФормат CSV: UTF-8 с BOM, разделитель — запятая.\n', 'Формат описан')
await wait(hasPending('approve', 'graph_outcome:passed'))
await openTask('Гейты')
await page.getByText('docker_fixme_check').first().waitFor()
await shot('tech-gates-passed')
await acceptTransition()

// ───────────────────────────────────────── implementation + delivery
log('Этап 4. Реализация: агент пишет код, план доставки утверждается по точному SHA-256')
await wait((detail, pending) => pending.some((a) => a.trigger === 'delivery_plan'), 180000)
await openTask()
await page.getByText('План доставки ожидает принятия').first().waitFor()
await shot('delivery-plan-awaiting-approval')
const plan = (await api.must('GET', `/runs/${runId}`)).approvals.find((a) => a.trigger === 'delivery_plan' && a.status === 'pending')
const ghBefore = readFileSync(path.join(home, 'state/gh.log'), 'utf8')
checks.check('до утверждения плана PR не создан (gh pr create не вызывался)', !ghBefore.includes('pr create'))
await click('Принять план доставки')
await wait((detail, pending) => pending.some((a) => a.trigger === 'graph_outcome:passed' && a.from_stage === 'code_review'), 240000)
const ghAfter = readFileSync(path.join(home, 'state/gh.log'), 'utf8')
checks.check('после утверждения контроллер создал PR (stub gh)', ghAfter.includes('pr create'))
checks.check('план утверждён по точному хешу', typeof plan.subject_hash === 'string' && plan.subject_hash.length === 64, plan.subject_hash)

// ───────────────────────────────────────── code_review (внешний Docker-исполнитель)
log('Этап 5. Код-ревью: внешний исполнитель в Docker, затем решение ревьюера')
await openTask()
await page.getByText('container-review').first().waitFor()
await shot('code-review-external-docker-executor')
let status = (await api.must('GET', `/runs/${runId}/stage-statuses`)).stages.find((s) => s.stage_id === 'code_review')
checks.check('исполнитель code_review — внешний агент container-review', status.executor.kind === 'external_agent' && status.executor.name === 'container-review', JSON.stringify(status.executor))
await acceptTransition()

// ───────────────────────────────────────── qa: провал, преждевременный Done из трекера, обход администратором
log('Этап 6. Тестирование: отчёт без «Result: PASS» — обязательный fail')
await wait(atStage('qa', 'results'))
await submitMarkdown('# Отчёт QA\n\nНайдено два дефекта в кодировке.\n', 'Прогон вручную')
await wait(hasPending('check_decision', 'stage_check'))

log('Трекер → ai-team: преждевременный «Done» не обходит провал гейта')
appendFileSync(path.join(home, 'tracker/inbox.jsonl'), `${JSON.stringify({ key: 'DEMO-1', status: 'Done', author: 'Иван (в трекере)' })}\n`)
await openTask('Трекер')
await click('Синхронизировать сейчас')
await page.getByText(/Done/).first().waitFor()
await shot('qa-gate-fail-premature-done-pending')
const inbound = await api.must('GET', '/tracker/inbound')
const request = (inbound.requests || inbound.items || inbound || [])[0]
checks.check('входящий «Done» записан как запрос в ожидании и ничего не сдвинул', request && ['pending', 'open'].includes(request.state ?? request.status), JSON.stringify(request ?? inbound).slice(0, 160))
const stillQa = await api.must('GET', `/runs/${runId}`)
checks.check('задача осталась на qa с проваленным обязательным гейтом', stillQa.state.next_stage === 'qa' && stillQa.approvals.some((a) => a.status === 'pending' && a.kind === 'check_decision'))

log('Админ обходит провал: причина обязательна, отметка красная')
await openTask()
await page.getByLabel('Причина обхода проверки').fill('Дефекты некритичны, релиз согласован с владельцем продукта')
await click('Обойти проверку')
await wait(hasPending('approve', 'graph_outcome:passed'))
await openTask('Гейты')
await page.getByText('qa_report_check').first().waitFor()
await shot('qa-admin-override-red-mark')
const overrides = (await api.must('GET', `/runs/${runId}/gates`)).overrides || []
checks.check('обход записан: автор, причина, гейт, хеш', overrides.length === 1 && overrides[0].actor_id && overrides[0].reason && overrides[0].artifact_sha256, JSON.stringify(overrides[0] ?? {}).slice(0, 200))
await acceptTransition()

// ───────────────────────────────────────── deploy
log('Этап 7. Выкладка: ссылка на сборку')
await wait(atStage('deploy', 'results'))
await openTask()
await click('Сдать результат')
await page.getByLabel('Ссылка на результат').fill('https://ci.example.test/build/42')
await page.getByLabel('Что сделано').fill('Сборка #42 выкачена на staging-заглушку')
await click('Сдать результат')
await wait(atStage('observation', 'results'))

// ───────────────────────────────────────── observation: технические ошибки (error) и повтор
log('Этап 8. Наблюдение: технический сбой гейтов (error) и повтор на той же версии')
writeFileSync(path.join(demoDir, 'docker-mode'), 'crash')
await submitMarkdown('# Наблюдение\n\nМетрики экспорта в норме.\n', 'Метрики за час')
await wait(hasPending('check_decision', 'stage_check'))
await openTask('Гейты')
await page.getByText('host_flaky_check').first().waitFor()
await shot('observation-host-gate-technical-error')
rows = await gateAttempts(api, runId, 'observation')
checks.check('host_flaky_check: execution=error без вердикта', rows.some((a) => a.gate_name === 'host_flaky_check' && a.execution === 'error' && !a.verdict), describeAttempts(rows))
await openTask()
await button(/^Повторить гейт: /).first().click()
for (let i = 0; i < 150; i += 1) {
  rows = await gateAttempts(api, runId, 'observation')
  if (rows.some((a) => a.gate_name === 'docker_flaky_check' && a.execution === 'error')) break
  await sleep(400)
}
await wait(hasPending('check_decision', 'stage_check'))
await openTask('Гейты')
await page.getByText('docker_flaky_check').first().waitFor()
await shot('observation-docker-gate-technical-error')
rows = await gateAttempts(api, runId, 'observation')
checks.check('docker_flaky_check: execution=error (контейнер упал), прежняя попытка host сохранена', rows.some((a) => a.gate_name === 'docker_flaky_check' && a.execution === 'error') && rows.filter((a) => a.gate_name === 'host_flaky_check').length === 2, describeAttempts(rows))
rmSync(path.join(demoDir, 'docker-mode'))
await openTask()
await button(/^Повторить гейт: /).first().click()
await wait(hasPending('approve', 'graph_outcome:passed'))
rows = await gateAttempts(api, runId, 'observation')
checks.check('после повтора оба гейта прошли на той же версии результата', rows.some((a) => a.gate_name === 'docker_flaky_check' && a.verdict === 'pass') && new Set(rows.map((a) => a.sha)).size === 1, describeAttempts(rows))
await acceptTransition()

// ───────────────────────────────────────── acceptance
log('Этап 9. Окончательная готовность: подтверждает владелец (bo)')
await wait(atStage('acceptance', 'results'))
await openTask()
await click('Принять')
await wait(hasPending('approve', 'graph_outcome:passed'))
await acceptTransition()
await wait(finished)
await openTask()
await page.getByText('Задача завершена').first().waitFor()
await shot('task-completed-all-stages')
const detail = await api.must('GET', `/runs/${runId}`)
checks.check('задача завершена', detail.run.status === 'completed', detail.run.status)

// ───────────────────────────────────────── итоги: статусы, посещения, артефакты, трекер, доставка
log('Статусы этапов, посещения и артефакты')
const statuses = await api.must('GET', `/runs/${runId}/stage-statuses`)
const byId = Object.fromEntries(statuses.stages.map((s) => [s.stage_id, s]))
checks.check('все 9 этапов в статусе passed', statuses.stages.length === 9 && statuses.stages.every((s) => s.status === 'passed'), statuses.stages.map((s) => `${s.stage_id}:${s.status}`).join(' '))
checks.check('у этапов с новыми версиями несколько посещений/попыток видны в истории', byId.product_spec.visits.length >= 1 && byId.tech_design.visits[0].attempts.length >= 3)
checks.check('executor implementation — внутренний агент, code_review — внешний', byId.implementation.executor.kind === 'internal_agent' && byId.code_review.executor.kind === 'external_agent')
const artifacts = await api.must('GET', `/runs/${runId}/events?limit=1000`)
checks.check('журнал событий задачи не пуст и содержит gate_attempt_finished/check_overridden', artifacts.events.some((e) => e.type === 'gate_attempt_finished') && artifacts.events.some((e) => e.type === 'check_overridden'))
// Задача терминальна раньше, чем контроллер записывает квитанцию Git-доставки (она выполняется после
// финализации): пока квитанции нет, карточка честно показывает «ждёт выполнения». Скриншот журнала
// снимаем после квитанции, иначе он показывает промежуточное состояние.
let deliveryStatus = ''
for (let i = 0; i < 120 && deliveryStatus !== 'recorded'; i++) {
  deliveryStatus = (await api.must('GET', `/runs/${runId}`)).delivery?.status ?? ''
  if (deliveryStatus !== 'recorded') await new Promise((resolve) => setTimeout(resolve, 500))
}
checks.check('квитанция Git-доставки записана контроллером (delivery.status=recorded)', deliveryStatus === 'recorded', deliveryStatus)
await openTask('Журнал')
await shot('journal')

log('Трекер: исходящие on_enter/on_pass (фейковый адаптер)')
const outbox = readFileSync(path.join(home, 'tracker/outbox.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map((line) => JSON.parse(line))
const transitions = outbox.filter((e) => e.op === 'transition').map((e) => e.status)
// Переход к статусу, в котором карточка уже стоит (после внешнего «Done»), ничего не отправляет, а
// устаревший on_enter пропускается (superseded): поэтому проверяем In Review и отсутствие сбоев доставки.
const trackerState = await api.must('GET', '/tracker')
checks.check('исходящий переход on_pass code_review → «In Review» дошёл до трекера', transitions.includes('In Review'), transitions.join(' → '))
checks.check('сбоев исходящей доставки нет', trackerState.failed_deliveries === 0, `failed_deliveries=${trackerState.failed_deliveries}`)
checks.check('к карточке ушли комментарии с исходами этапов, обход назван как обход', outbox.filter((e) => e.op === 'comment').length >= 9 && outbox.some((e) => e.op === 'comment' && String(e.comment).includes('outcome=overridden')), `${outbox.filter((e) => e.op === 'comment').length} комментариев`)
await page.goto(`${baseURL}/tracker`)
await page.getByRole('heading', { name: /Трекер/ }).first().waitFor()
await shot('tracker-overview')

log('Доставка (delivery): ветка запушена контроллером в локальный bare-репозиторий')
const remoteBranches = (await import('node:child_process')).execFileSync('git', ['--git-dir', path.join(home, 'state/origin.git'), 'branch', '--list', 'ai-team/*'], { encoding: 'utf8' })
checks.check('в origin появилась ветка ai-team/csv-export', remoteBranches.includes('ai-team/csv-export'), remoteBranches.trim())
const runEvents = readFileSync(path.join(home, 'repo/.ai-team/runs', runId, 'events.jsonl'), 'utf8')
checks.check('в неизменяемом журнале run есть delivery_plan_approved', runEvents.includes('"type":"delivery_plan_approved"'))
}

// ───────────────────────────────────────── цикл код-ревью: внешний и внутренний исполнитель
async function reviewLoop(kind, label, title) {
  log(`${label}: публикация короткого шаблона и задача (changes requested → возврат → одобрение)`)
  const yaml = readFileSync(path.join(repoRoot, `docs/demo/demo-template-review-loop-${kind}.yaml`), 'utf8')
  // Из веба нельзя убрать гейты файлового конфига, поэтому короткий шаблон сначала становится
  // файловым конфигом (правка кода человеком), а страница «Шаблон» публикует тот же документ.
  writeFileSync(path.join(home, 'repo/.ai-team/config.yaml'), yaml)
  await page.goto(`${baseURL}/template`)
  await page.getByLabel('Конфигурация выбранного шаблона').fill(yaml)
  await click('Проверить YAML')
  await page.getByText(/валид|корректн|Шаблон проходит/i).first().waitFor({ timeout: 15000 }).catch(() => {})
  await button(/опубликовать/i, false).first().click()
  await page.waitForTimeout(1200)
  await shot(`template-published-${kind}`)
  writeMode('coder-mode', 'fixme-first')
  writeMode('coder-calls', '0')
  await page.goto(`${baseURL}/tasks/new`)
  await page.selectOption('#task-project', { label: 'Демо S17' })
  await page.getByLabel('Название').fill(title)
  await page.getByLabel('Идентификатор').fill(`loop-${kind}`)
  await page.getByLabel('Текст идеи').fill('Обработать пустой ввод в экспорте.')
  await click('Создать задачу')
  await page.getByRole('link', { name: 'Открыть задачу' }).click()
  await page.waitForURL(/\/tasks\/\d{8}T/)
  runId = new URL(page.url()).pathname.split('/').pop()
  await wait(hasPending(null, 'graph_outcome:rejected'), 180000)
  await openTask()
  await page.getByText(/changes requested|CHANGES_REQUESTED|Вернуть на доработку/i).first().waitFor()
  await shot(`review-loop-${kind}-changes-requested`)
  const review = await api.must('GET', `/runs/${runId}/stage-statuses`)
  const reviewStage = review.stages.find((s) => s.stage_id === 'code_review')
  checks.check(`${label}: ревьюер потребовал изменений (executor ${reviewStage.executor.kind})`, reviewStage.executor.kind === (kind === 'external' ? 'external_agent' : 'internal_agent'), JSON.stringify(reviewStage.executor))
  await page.getByLabel('Причина возврата').fill('Убрать FIXME и обработать пустой ввод')
  await button(/^Вернуть на доработку/, false).first().click()
  await wait(hasPending('approve', 'graph_outcome:passed'), 180000)
  await openTask()
  await shot(`review-loop-${kind}-approved-after-return`)
  await acceptTransition()
  await wait(finished)
  const done = (await api.must('GET', `/runs/${runId}/stage-statuses`)).stages
  const implementation = done.find((s) => s.stage_id === 'implementation')
  const reviewDone = done.find((s) => s.stage_id === 'code_review')
  checks.check(`${label}: возврат на implementation, второе посещение, одобрение`, implementation.visit_count === 2 && reviewDone.visit_count === 2 && reviewDone.status === 'passed', `implementation visits=${implementation.visit_count}, code_review visits=${reviewDone.visit_count}`)
}
async function loopsFlow() {
log('Шаблон с командными гейтами через веб отклоняется (они разрешены только в файловом конфиге)')
{
  const active = await api.must('GET', '/template')
  const gated = readFileSync(path.join(repoRoot, 'docs/demo/demo-template.yaml'), 'utf8')
  const refused = await api.request('POST', '/template/publish', { yaml: gated, expected_version: active.template.version })
  checks.check('публикация шаблона с external_program/docker_program через REST отклонена: 400 trusted_config_only',
    refused.status === 400 && refused.data?.code === 'trusted_config_only', `${refused.status} ${refused.data?.code}`)
  const after = await api.must('GET', '/template')
  checks.check('активный шаблон после отказа не изменился', after.template.version === active.template.version)
}
await reviewLoop('external', 'Внешний Docker-исполнитель code_review', 'Цикл код-ревью: контейнер')
await reviewLoop('internal', 'Внутренний агент code_review', 'Цикл код-ревью: внутренний агент')

log('Лента событий и очередь')
await page.goto(`${baseURL}/events`)
await page.waitForTimeout(800)
await shot('events-feed')
await page.goto(baseURL)
await shot('queue')

}

try {
  if (phase !== 'loops') await mainFlow()
  if (phase !== 'main') await loopsFlow()
} catch (error) {
  // Состояние при ошибке должно быть понятным: что было на экране и что отвечал сервер.
  console.error(`\n✗ сценарий остановился: ${error.message.split('\n')[0]}`)
  try {
    await page.screenshot({ path: path.join(home, `failure-${phase}.png`), fullPage: true })
    console.error(`  снимок экрана: ${path.join(home, `failure-${phase}.png`)}`)
    console.error(`  кнопки: ${JSON.stringify(await page.getByRole('button').allInnerTexts())}`)
    console.error(`  текст страницы: ${(await page.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 1200)}`)
    if (runId) {
      const { detail, pending } = await (await import('./lib/flow.mjs')).snapshot(api, runId)
      console.error(`  состояние: status=${detail.run.status} phase=${detail.state?.phase} stage=${detail.state?.next_stage} pending=${JSON.stringify(pending.map((a) => `${a.kind}:${a.trigger}`))}`)
    }
  } catch (dumpError) { console.error(`  (не удалось снять состояние: ${dumpError.message})`) }
  await browser.close()
  process.exit(1)
}

await browser.close()
const ok = checks.summary()
writeFileSync(path.join(home, `ui-result-${phase}.json`), JSON.stringify({ ok, runs: runId, checks: checks.rows }, null, 2))
process.exit(ok ? 0 : 1)
