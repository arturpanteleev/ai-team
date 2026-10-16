#!/usr/bin/env node
// run-mcp.mjs — ключевые действия демонстрации S17 по сетевому MCP.
//
//   node scripts/demo/run-mcp.mjs --home <DEMO_HOME> --port <PORT>
//
// Повторяет по MCP то, что run-ui.mjs делал в браузере, на новой задаче того же
// шаблона: кодовый fail и новая версия, LLM-гейт и решение PO, host- и
// Docker-гейты, план доставки по точному SHA-256, внешний Docker-исполнитель
// code_review, обход администратором, повтор технического сбоя, связь с
// трекером. Токен — локальный токен сервера (персональные токены aitm_ выпускает
// только браузерная сессия в облачном режиме, см. docs/reference/mcp.md).
import { appendFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { Checklist, atStage, finished, hasPending, openApi, settle } from './lib/flow.mjs'
import { Mcp } from './lib/mcp.mjs'

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, value, index, all) => (value.startsWith('--') ? [...acc, [value.slice(2), all[index + 1]]] : acc), []))
const home = path.resolve(args.home ?? process.env.DEMO_HOME ?? '')
const port = args.port ?? process.env.DEMO_PORT ?? '18080'
const { token, baseURL, api } = openApi(home, port)
await api.login() // только для чтения состояния (ожидание); действия — по MCP
const mcp = new Mcp(baseURL, token)
const checks = new Checklist()
const log = (text) => console.log(`\n▶ ${text}`)
const demoDir = path.join(home, 'repo', '.demo')
mkdirSync(demoDir, { recursive: true })
writeFileSync(path.join(home, 'state', 'coder-mode'), 'clean')

const tools = await mcp.tools()
checks.check('MCP: список инструментов получен', tools.length > 60, `${tools.length} инструментов`)
const me = await mcp.must('whoami')
checks.check('MCP: whoami возвращает локального владельца', JSON.stringify(me).includes('local-user'))

let runId = ''
const wait = (predicate, timeout) => settle(api, runId, predicate, timeout)
const run = (stage) => ({ run_id: runId, stage_id: stage })
const sha = (pending, trigger) => pending.find((a) => a.trigger === trigger)

log('Проект и задача по MCP')
const project = await mcp.must('create_project', { name: 'Демо S17 (MCP)', description: 'Те же действия по сетевому MCP' })
const started = await mcp.must('start_task', { project_id: project.id, feature: 'csv-export-mcp', task: 'Экспорт отчёта в CSV (по MCP)', title: 'Экспорт CSV через MCP' })
runId = started.run_id
await wait(atStage('intent', 'results'))
await mcp.must('set_task_tracker_link', { run_id: runId, issue_key: 'DEMO-2' })
const link = await mcp.must('get_task_tracker', { run_id: runId })
checks.check('MCP: связь с карточкой DEMO-2 создана', JSON.stringify(link).includes('DEMO-2'))

log('Бизнес-идея и спецификация: кодовый fail → новая версия → LLM → решение PO')
await mcp.must('submit_stage_result', { ...run('intent'), result: 'approve', note: 'Идея подтверждена' })
await wait(atStage('product_spec', 'results'))
await mcp.must('submit_stage_result', { ...run('product_spec'), result: 'md', content: '# Спека\n\nбез критериев\n', description: 'v1' })
let st = await wait(hasPending('check_decision', 'stage_check'))
const gates1 = await mcp.must('get_task_gates', { run_id: runId })
checks.check('MCP: get_task_gates показывает fail required_sections', JSON.stringify(gates1).includes('required_sections') && JSON.stringify(gates1).includes('"fail"'))
const check = st.pending.find((a) => a.kind === 'check_decision')
await mcp.must('decide_approval', { run_id: runId, approval_id: check.id, action: 'new_version', subject_hash: check.subject_hash, actor_role: check.required_roles[0] })
await mcp.must('resume_task', { run_id: runId })
await wait(hasPending('input', 'human_input'))
await mcp.must('submit_stage_result', { ...run('product_spec'), result: 'md', content: '# Спека\n\n## Критерии приёмки\n- Файл открывается в Excel\n', description: 'v2' })
st = await wait(hasPending('approve', 'graph_outcome:passed'))
await mcp.must('confirm_stage', { ...run('product_spec'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })

log('Архитектура: host-гейт (TODO) и Docker-гейт (FIXME)')
await wait(atStage('tech_design', 'results'))
for (const [content, expect] of [['# Архитектура\n\nTODO\n', 'host'], ['# Архитектура\n\nFIXME\n', 'docker']]) {
  await mcp.must('submit_stage_result', { ...run('tech_design'), result: 'md', content, description: expect })
  st = await wait(hasPending('check_decision', 'stage_check'))
  const failed = st.pending.find((a) => a.kind === 'check_decision')
  await mcp.must('decide_approval', { run_id: runId, approval_id: failed.id, action: 'new_version', subject_hash: failed.subject_hash, actor_role: failed.required_roles[0] })
  await mcp.must('resume_task', { run_id: runId })
  await wait(hasPending('input', 'human_input'))
}
await mcp.must('submit_stage_result', { ...run('tech_design'), result: 'md', content: '# Архитектура\n\nФормат описан.\n', description: 'v3' })
st = await wait(hasPending('approve', 'graph_outcome:passed'))
await mcp.must('confirm_stage', { ...run('tech_design'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })

log('Реализация: план доставки по точному хешу')
st = await wait((d, p) => p.some((a) => a.trigger === 'delivery_plan'), 180000)
const plan = sha(st.pending, 'delivery_plan')
const wrong = await mcp.tool('approve_delivery_plan', { run_id: runId, plan_hash: '0'.repeat(64) })
checks.check('MCP: неверный хеш плана отклонён, ничего не доставлено', wrong.isError && !readFileSync(path.join(home, 'state/gh.log'), 'utf8').split('\n').some((l) => l.startsWith('pr create') && false))
await mcp.must('approve_delivery_plan', { run_id: runId, plan_hash: plan.subject_hash })
st = await wait((d, p) => p.some((a) => a.trigger === 'graph_outcome:passed' && a.from_stage === 'code_review'), 240000)
const review = await mcp.must('get_task_stage_statuses', { run_id: runId })
checks.check('MCP: исполнитель code_review — внешний агент container-review', JSON.stringify(review).includes('container-review'))
await mcp.must('confirm_stage', { ...run('code_review'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })

log('Тестирование: обход проваленного гейта администратором')
await wait(atStage('qa', 'results'))
await mcp.must('submit_stage_result', { ...run('qa'), result: 'md', content: '# QA\n\nдефекты\n', description: 'qa' })
await wait(hasPending('check_decision', 'stage_check'))
await mcp.must('override_stage_gate', { ...run('qa'), reason: 'Согласовано владельцем продукта (MCP)' })
st = await wait(hasPending('approve', 'graph_outcome:passed'))
const overrides = (await api.must('GET', `/runs/${runId}/gates`)).overrides || []
checks.check('MCP: обход записан с причиной', overrides.length === 1 && overrides[0].reason.includes('MCP'))
await mcp.must('confirm_stage', { ...run('qa'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })

log('Выкладка и наблюдение: технический сбой гейта и повтор')
await wait(atStage('deploy', 'results'))
await mcp.must('submit_stage_result', { ...run('deploy'), result: 'link', url: 'https://ci.example.test/build/43', link_kind: 'build' })
await wait(atStage('observation', 'results'))
writeFileSync(path.join(demoDir, 'docker-mode'), 'crash')
rmSync(path.join(demoDir, 'host-flaky-fired'), { force: true })
await mcp.must('submit_stage_result', { ...run('observation'), result: 'md', content: '# Наблюдение\n\nВсё в норме.\n', description: 'obs' })
await wait(hasPending('check_decision', 'stage_check'))
await mcp.must('retry_stage_gate', { ...run('observation'), gate: 'host_flaky_check' })
await wait(hasPending('check_decision', 'stage_check'))
rmSync(path.join(demoDir, 'docker-mode'), { force: true })
await mcp.must('retry_stage_gate', { ...run('observation'), gate: 'docker_flaky_check' })
st = await wait(hasPending('approve', 'graph_outcome:passed'))
await mcp.must('confirm_stage', { ...run('observation'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })
await wait(atStage('acceptance', 'results'))
await mcp.must('submit_stage_result', { ...run('acceptance'), result: 'approve', note: 'Принято' })
st = await wait(hasPending('approve', 'graph_outcome:passed'))
await mcp.must('confirm_stage', { ...run('acceptance'), subject_hash: sha(st.pending, 'graph_outcome:passed').subject_hash })
await wait(finished)

const statuses = await mcp.must('get_task_stage_statuses', { run_id: runId })
const list = statuses.stages ?? statuses
checks.check('MCP: все 9 этапов passed, то же, что в UI', list.length === 9 && list.every((s) => s.status === 'passed'))
const events = await mcp.must('list_task_events', { run_id: runId, limit: 1000 })
const audit = (events.events || []).filter((e) => e.type === 'mcp_action')
checks.check('MCP: каждое действие оставило запись mcp_action (канал mcp)', audit.length >= 15, `${audit.length} записей`)
const outbox = readFileSync(path.join(home, 'tracker/outbox.jsonl'), 'utf8')
checks.check('MCP: трекер получил переходы по задаче DEMO-2', outbox.includes('"key":"DEMO-2"'))
appendFileSync(path.join(home, 'tracker/inbox.jsonl'), '')

const ok = checks.summary()
writeFileSync(path.join(home, 'mcp-result.json'), JSON.stringify({ ok, run: runId, checks: checks.rows }, null, 2))
process.exit(ok ? 0 : 1)
