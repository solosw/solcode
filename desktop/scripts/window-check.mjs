/**
 * End-to-end window check over the Chrome DevTools Protocol.
 *
 * Launches the real Electron app, attaches to its renderer, and drives one
 * prompt through the preload bridge exactly as a user would. This is the only
 * check that covers the whole stack: window → preload → IPC → ACP client →
 * solcode → streaming updates → rendered DOM.
 */

import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { resolve } from 'node:path'

const appDir = resolve(import.meta.dirname, '..')
const electron = resolve(appDir, 'node_modules', 'electron', 'dist', 'electron.exe')
if (!existsSync(electron)) {
  console.log(`FAIL: electron binary missing at ${electron}`)
  process.exit(1)
}

const DEBUG_PORT = 9333
const child = spawn(electron, ['.', `--remote-debugging-port=${DEBUG_PORT}`], {
  cwd: appDir,
  stdio: ['ignore', 'pipe', 'pipe'],
})

let stderr = ''
child.stdout.on('data', d => { process.stdout.write(d) })
child.stderr.on('data', (d) => { stderr += d.toString() })

/** Poll the DevTools endpoint until the renderer target appears. */
async function attach() {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    await new Promise(r => setTimeout(r, 500))
    try {
      const response = await fetch(`http://127.0.0.1:${DEBUG_PORT}/json/list`)
      const targets = await response.json()
      const page = targets.find(t => t.type === 'page' && typeof t.webSocketDebuggerUrl === 'string')
      if (page !== undefined) return page.webSocketDebuggerUrl
    } catch { /* the port is not listening yet */ }
  }
  throw new Error('renderer target never appeared')
}

/** Minimal CDP client: evaluate expressions in the page and read results. */
class Cdp {
  #socket
  #id = 0
  #pending = new Map()

  static async connect(url) {
    const client = new Cdp()
    client.#socket = new WebSocket(url)
    await new Promise((resolve, reject) => {
      client.#socket.addEventListener('open', () => { resolve() }, { once: true })
      client.#socket.addEventListener('error', () => { reject(new Error('cdp socket error')) }, { once: true })
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

  /** Evaluate an expression in the page and return its JSON value. */
  async evaluate(expression) {
    const id = ++this.#id
    const response = new Promise((resolve) => { this.#pending.set(id, { resolve }) })
    this.#socket.send(JSON.stringify({
      id,
      method: 'Runtime.evaluate',
      params: { expression, awaitPromise: true, returnByValue: true },
    }))
    const message = await response
    if (message.result?.exceptionDetails !== undefined) {
      throw new Error(message.result.exceptionDetails.text ?? 'evaluation failed')
    }
    return message.result?.result?.value
  }

  close() { this.#socket.close() }
}

const FAILURES = []
function check(label, condition, detail) {
  console.log(`${condition ? 'ok  ' : 'FAIL'} ${label}${detail === undefined ? '' : ` — ${JSON.stringify(detail)}`}`)
  if (!condition) FAILURES.push(label)
}

try {
  const socketUrl = await attach()
  const cdp = await Cdp.connect(socketUrl)

  // The preload bridge must exist before anything else can work.
  const bridgeKeys = await cdp.evaluate('Object.keys(window.solcode || {})')
  check('preload bridge exposed', Array.isArray(bridgeKeys) && bridgeKeys.includes('prompt'), bridgeKeys)

  // Live DOM: the shell rendered, not just an empty document.
  const title = await cdp.evaluate('document.querySelector("aside")?.innerText?.slice(0,60)')
  check('sidebar rendered', typeof title === 'string' && title.includes('Solcode'), title)

  // Wait for the agent handshake to settle into a usable session.
  let ready = false
  for (let attempt = 0; attempt < 40 && !ready; attempt += 1) {
    await new Promise(r => setTimeout(r, 500))
    const snapshot = await cdp.evaluate('window.solcode.getSnapshot()')
    if (snapshot?.status === 'ready' && snapshot?.sessionId) {
      ready = true
      check('session ready', true, { sessionId: snapshot.sessionId, modes: snapshot.modes.length, commands: snapshot.commands.length })
      check('modes advertised', snapshot.modes.length >= 5, snapshot.modes.map(m => m.id))
    } else if (snapshot?.status === 'error' || snapshot?.status === 'exited') {
      check('session ready', false, snapshot.error)
      break
    }
  }
  if (!ready) check('session ready', false, 'timed out')

  if (ready) {
    // Drive a real turn through the same path the composer uses.
    await cdp.evaluate('window.solcode.prompt("/status")')
    let answered = false
    for (let attempt = 0; attempt < 60 && !answered; attempt += 1) {
      await new Promise(r => setTimeout(r, 500))
      const text = await cdp.evaluate(`(() => {
        const s = document.querySelector('.prose-solcode')
        return s ? s.innerText.slice(0, 80) : null
      })()`)
      if (typeof text === 'string' && text.length > 0) {
        answered = true
        check('assistant answer rendered in DOM', true, text.replace(/\n/g, ' | '))
      }
    }
    if (!answered) check('assistant answer rendered in DOM', false, 'no prose node appeared')

    // The tool card path is exercised by a plan write; assert the entry point exists.
    const hasComposer = await cdp.evaluate('!!document.querySelector("textarea")')
    check('composer present', hasComposer === true)
  }

  cdp.close()
  console.log(FAILURES.length === 0 ? '\nWINDOW_CHECK: PASS' : `\nWINDOW_CHECK: FAIL (${FAILURES.join(', ')})`)
  child.kill()
  process.exit(FAILURES.length === 0 ? 0 : 1)
} catch (cause) {
  console.log(`\nFAIL: ${cause instanceof Error ? cause.message : String(cause)}`)
  console.log('--- stderr tail ---')
  console.log(stderr.slice(-3000))
  console.log('\nWINDOW_CHECK: FAIL')
  child.kill()
  process.exit(1)
}
