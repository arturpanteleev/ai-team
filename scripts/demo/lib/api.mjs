// Минимальный клиент REST ai-team web для демо-скриптов: вход по локальному токену
// (POST /api/session → cookie + CSRF), затем обычные запросы. Это тот же вход, что
// делает браузер; скрытых маршрутов здесь нет.
export class Api {
  constructor(baseURL, token) {
    this.baseURL = baseURL
    this.token = token
    this.cookie = ''
    this.csrf = ''
  }

  async login() {
    const response = await fetch(`${this.baseURL}/api/session`, { method: 'GET', headers: { Authorization: `Bearer ${this.token}` } })
    if (!response.ok) throw new Error(`login failed: ${response.status} ${await response.text()}`)
    this.cookie = (response.headers.getSetCookie?.() ?? []).map((value) => value.split(';')[0]).join('; ')
    this.csrf = (await response.json()).csrf_token
    return this
  }

  async request(method, path, body, extraHeaders = {}) {
    const headers = { Cookie: this.cookie, ...extraHeaders }
    if (method !== 'GET') headers['X-CSRF-Token'] = this.csrf
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const response = await fetch(`${this.baseURL}/api${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
    const text = await response.text()
    let data = text
    try { data = text ? JSON.parse(text) : null } catch { /* plain text answer */ }
    return { status: response.status, ok: response.ok, data }
  }

  get(path) { return this.request('GET', path) }
  post(path, body) { return this.request('POST', path, body ?? {}) }
  put(path, body) { return this.request('PUT', path, body ?? {}) }

  // ok-вариант: бросает на не-2xx и называет ответ сервера.
  async must(method, path, body) {
    const result = await this.request(method, path, body)
    if (!result.ok) throw new Error(`${method} ${path} -> ${result.status}: ${JSON.stringify(result.data)}`)
    return result.data
  }
}
