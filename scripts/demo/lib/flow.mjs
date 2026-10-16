// Общие помощники демо-скриптов: ожидание «спокойного» состояния задачи по REST
// (только чтение) и проверка итогов. Действия через эти помощники не выполняются.
import { readFileSync } from 'node:fs'
import { Api } from './api.mjs'

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export function openApi(home, port) {
  const token = readFileSync(`${home}/repo/.ai-team/web.token`, 'utf8').trim()
  return { token, baseURL: `http://127.0.0.1:${port}`, api: new Api(`http://127.0.0.1:${port}`, token) }
}

// Читает задачу и её ожидающие согласования.
export async function snapshot(api, runId) {
  const detail = await api.must('GET', `/runs/${runId}`)
  const pending = (detail.approvals || []).filter((value) => value.status === 'pending')
  return { detail, pending }
}

// Ждёт, пока контроллер закончит работу (задача ждёт человека, остановлена или
// завершена) и выполнится predicate. Бросает ошибку с состоянием по таймауту.
export async function settle(api, runId, predicate = () => true, timeoutMs = 120000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const { detail, pending } = await snapshot(api, runId)
    const calm = ['waiting', 'resumable', 'terminal'].includes(detail.state?.phase) && detail.run.status !== 'running'
    if (calm && predicate(detail, pending)) return { detail, pending }
    if (Date.now() > deadline) {
      throw new Error(`задача не пришла в ожидаемое состояние: status=${detail.run.status} phase=${detail.state?.phase} stage=${detail.state?.next_stage} pending=${JSON.stringify(pending.map((a) => `${a.kind}:${a.trigger}`))}`)
    }
    await sleep(400)
  }
}

export const hasPending = (kind, trigger) => (_detail, pending) => pending.some((a) => (!kind || a.kind === kind) && (!trigger || a.trigger === trigger))
export const atStage = (stage, action) => (detail) => detail.state?.next_stage === stage && (!action || (detail.allowed_actions || []).includes(action))
export const finished = (detail) => detail.state?.phase === 'terminal'

// Попытки гейтов одной стадии: плоский список с версией результата.
export async function gateAttempts(api, runId, stage) {
  const gates = await api.must('GET', `/runs/${runId}/gates`)
  const entry = (gates.stages || []).find((value) => value.stage_id === stage)
  const rows = []
  for (const wave of entry?.waves || []) {
    for (const attempt of wave.attempts || []) rows.push({ ...attempt, visit: wave.visit, sha: (wave.artifact_sha256 || '').slice(0, 8) })
  }
  return rows
}

export function describeAttempts(rows) {
  return rows.map((a) => `${a.gate_name}[${a.mode}] ${a.execution}/${a.verdict ?? '-'}`).join('; ')
}

// Список проверок итогов: накапливает строки и падает в конце, если хоть одна не сошлась.
export class Checklist {
  constructor() { this.rows = [] }
  check(title, ok, detail = '') {
    this.rows.push({ title, ok: Boolean(ok), detail })
    console.log(`  ${ok ? '✓' : '✗'} ${title}${detail ? ` — ${detail}` : ''}`)
  }
  get failed() { return this.rows.filter((row) => !row.ok) }
  summary() {
    console.log(`\nИтог: ${this.rows.length - this.failed.length}/${this.rows.length} проверок прошли`)
    for (const row of this.failed) console.log(`  ✗ ${row.title} ${row.detail}`)
    return this.failed.length === 0
  }
}
