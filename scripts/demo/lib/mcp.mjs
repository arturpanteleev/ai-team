// Тонкий клиент сетевого MCP ai-team (stateless Streamable HTTP, версия 2026-07-28)
// для демо-скриптов: JSON-RPC по POST /mcp с Bearer-токеном. Это не замена
// настоящим клиентам (claude, codex — см. run-real-clients.sh), а способ повторить
// те же вызовы в скрипте и проверить ответы.
import { randomUUID } from 'node:crypto'

export class Mcp {
  constructor(baseURL, token, client = 'ai-team-demo-script/1') {
    this.url = `${baseURL}/mcp`
    this.token = token
    this.client = client
    this.id = 0
    this.keyed = new Set()
  }

  async rpc(method, params = {}) {
    this.id += 1
    const body = {
      jsonrpc: '2.0', id: this.id, method,
      params: { ...params, _meta: { 'io.modelcontextprotocol/protocolVersion': '2026-07-28', 'io.modelcontextprotocol/clientCapabilities': {} } },
    }
    const response = await fetch(this.url, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${this.token}`, 'Content-Type': 'application/json',
        Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2026-07-28', 'User-Agent': this.client,
        'Mcp-Method': method, ...(params.name ? { 'Mcp-Name': params.name } : {}),
      },
      body: JSON.stringify(body),
    })
    const payload = await response.json()
    if (payload.error) throw new Error(`MCP ${method}: ${payload.error.code} ${payload.error.message}`)
    return payload.result
  }

  async tools() {
    const result = await this.rpc('tools/list')
    this.keyed = new Set(result.tools.filter((tool) => tool.inputSchema?.properties?.idempotency_key).map((tool) => tool.name))
    return result.tools
  }

  // Вызывает инструмент; ключ идемпотентности добавляется сам там, где он нужен.
  async tool(name, args = {}, { key } = {}) {
    if (this.keyed.size === 0) await this.tools()
    const arguments_ = { ...args }
    if (this.keyed.has(name) && !arguments_.idempotency_key) arguments_.idempotency_key = key ?? randomUUID()
    const result = await this.rpc('tools/call', { name, arguments: arguments_ })
    const text = (result.content || []).filter((part) => part.type === 'text').map((part) => part.text).join('')
    let data = text
    try { data = JSON.parse(text) } catch { /* ответ не JSON */ }
    return { isError: Boolean(result.isError), data, text, meta: result._meta ?? {} }
  }

  // Бросает, если инструмент вернул isError.
  async must(name, args, options) {
    const result = await this.tool(name, args, options)
    if (result.isError) throw new Error(`MCP ${name}: ${result.text.slice(0, 300)}`)
    return result.data
  }
}
