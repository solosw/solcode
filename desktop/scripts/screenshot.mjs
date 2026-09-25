/**
 * Capture the running app's window to a PNG for visual review.
 *
 * Drives a turn first so the screenshot shows a populated transcript rather
 * than the empty state.
 */

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const appDir = resolve(import.meta.dirname, '..')
const electron = resolve(appDir, 'node_modules', 'electron', 'dist', 'electron.exe')
if (!existsSync(electron)) { console.log('FAIL: electron missing'); process.exit(1) }

const outDir = resolve(appDir, '.verify')
mkdirSync(outDir, { recursive: true })

const DEBUG_PORT = 9334
const child = spawn(electron, ['.', `--remote-debugging-port=${DEBUG_PORT}`], {
  cwd: appDir,
  stdio: ['ignore', 'ignore', 'pipe'],
})

async function attach() {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    await new Promise(r => setTimeout(r, 500))
    try {
      const response = await fetch(`http://127.0.0.1:${DEBUG_PORT}/json/list`)
      const targets = await response.json()
      const page = targets.find(t => t.type === 'page' && typeof t.webSocketDebuggerUrl === 'string')
      if (page !== undefined) return page.webSocketDebuggerUrl
    } catch { /* not up yet */ }
  }
  throw new Error('no renderer target')
}

class Cdp {
  #socket; #id = 0; #pending = new Map()
  static async connect(url) {
    const client = new Cdp()
    client.#socket = new WebSocket(url)
    await new Promise((res, rej) => {
      client.#socket.addEventListener('open', () => { res() }, { once: true })
      client.#socket.addEventListener('error', () => { rej(new Error('cdp error')) }, { once: true })
    })
    client.#socket.addEventListener('message', (event) => {
      const message = JSON.parse(typeof event.data === 'string' ? event.data : '')
      const call = client.#pending.get(message.id)
      if (call === undefined) return
      client.#pending.delete(message.id)
      call.resolve(message)
    })
    return client
  }
  async send(method, params) {
    const id = ++this.#id
    const response = new Promise(resolve => { this.#pending.set(id, { resolve }) })
    this.#socket.send(JSON.stringify({ id, method, params }))
    return await response
  }
  async evaluate(expression) {
    const message = await this.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
    return message.result?.result?.value
  }
  close() { this.#socket.close() }
}

try {
  const cdp = await Cdp.connect(await attach())

  // Wait for a live session, then produce a transcript worth looking at.
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const snapshot = await cdp.evaluate('window.solcode.getSnapshot()')
    if (snapshot?.status === 'ready' && snapshot?.sessionId) break
    await new Promise(r => setTimeout(r, 500))
  }
  await cdp.evaluate('window.solcode.prompt("/status")')
  await new Promise(r => setTimeout(r, 4000))
  await cdp.evaluate('window.solcode.prompt("Explain in two sentences what this project is, then run the bash command `echo hello`. Use a tool.")')
  // Poll until the turn actually finishes; a fixed sleep makes the screenshot
  // race the agent and can capture a half-rendered transcript.
  for (let attempt = 0; attempt < 120; attempt += 1) {
    await new Promise(r => setTimeout(r, 1000))
    const snapshot = await cdp.evaluate('window.solcode.getSnapshot()')
    if (snapshot?.status !== 'prompting') break
  }
  await new Promise(r => setTimeout(r, 1000))

  const summary = await cdp.evaluate(`(() => {
    return { proseNodes: document.querySelectorAll('.prose-solcode').length,
             toolCards: document.querySelectorAll('[data-tool-card]').length,
             text: (document.querySelector('.prose-solcode')?.innerText || '').slice(0, 200) }
  })()`)
  console.log('DOM summary:', JSON.stringify(summary))

  const { result } = await cdp.send('Page.captureScreenshot', { format: 'png' })
  if (typeof result?.data === 'string') {
    writeFileSync(resolve(outDir, 'window.png'), Buffer.from(result.data, 'base64'))
    console.log(`saved ${resolve(outDir, 'window.png')}`)
  } else {
    console.log('FAIL: no screenshot data')
  }
  cdp.close()
  child.kill()
  process.exit(0)
} catch (cause) {
  console.log(`FAIL: ${cause instanceof Error ? cause.message : String(cause)}`)
  child.kill()
  process.exit(1)
}
