import test from 'node:test'
import assert from 'node:assert/strict'
import { readConfigOptions, hasConfigValue } from '../dist/shared/config-options.js'
import { AgentSession } from '../dist/main/agent-session.js'
import { AcpClient } from '../dist/main/acp-client.js'

const option = (id, category, currentValue, values) => ({
  id, category, type: 'select', name: id, currentValue,
  options: values.map(value => ({ value, name: value })),
})
const initial = () => [
  option('agent-mode', 'mode', 'auto', ['auto', 'plan']),
  option('provider-model', 'model', 'a/same', ['a/same', 'b/same']),
  option('reasoning', 'thought_level', 'high', ['low', 'high']),
]
const deferred = () => {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const tick = () => new Promise(resolve => setImmediate(resolve))
function fixture() {
  const calls = []
  const client = { setConfigOption: (...args) => {
    const gate = deferred()
    calls.push({ args, ...gate })
    return gate.promise
  } }
  const session = new AgentSession(client)
  session.adoptSession({ sessionId: 's1', configOptions: initial(), sequence: 1 }, '/tmp')
  return { calls, session }
}

test('reads opaque IDs, grouped values and unknown categories; ignores unsupported types', () => {
  const grouped = {
    ...option('opaque', 'custom-category', 'b/same', []),
    options: [{ group: 'b', name: 'Provider B', options: [{ value: 'b/same', name: 'Same model' }] }],
  }
  const options = readConfigOptions([
    grouped, { ...grouped }, { type: 'boolean', id: 'toggle', currentValue: false },
    { ...grouped, id: 'bad', currentValue: 'missing' },
    option('thinking-only', 'thought_level', 'low', ['low']),
    option('no-category', undefined, 'one', ['one']),
  ])
  assert.equal(options.length, 3)
  assert.equal(options[0].options[0].name, 'Provider B')
  assert.ok(hasConfigValue(options[0], 'b/same'))
  assert.equal(options[1].category, 'thought_level')
  assert.equal(options[2].category, undefined)
  assert.deepEqual(readConfigOptions([]), [])
})

test('serial mutations use actual IDs and revalidate dependent options on the latest full snapshot', async () => {
  const { calls, session } = fixture()
  const model = session.setConfigOption('provider-model', 'b/same')
  const effort = session.setConfigOption('reasoning', 'low')
  const rejectedEffort = assert.rejects(effort, /no longer available/)
  await tick()
  assert.equal(calls.length, 1)
  assert.deepEqual(calls[0].args, ['s1', 'provider-model', 'b/same'])
  assert.equal(session.snapshot().configOptions[1].currentValue, 'a/same')
  assert.equal(session.snapshot().configPending, 2)
  const next = initial().slice(0, 2)
  next[1].currentValue = 'b/same'
  calls[0].resolve({ configOptions: next, sequence: 2 })
  await model
  await rejectedEffort
  assert.equal(calls.length, 1)
  assert.equal(session.snapshot().configOptions.length, 2)
  assert.equal(session.snapshot().configPending, 0)
  await assert.rejects(session.waitForConfiguration())
})

test('failed changes keep confirmed state and block prompts; explicit retry recovers', async () => {
  const { calls, session } = fixture()
  const change = session.setConfigOption('agent-mode', 'plan')
  const rejected = assert.rejects(change, /unavailable/)
  const waiting = assert.rejects(session.waitForConfiguration(), /unavailable/)
  await tick()
  calls[0].reject(new Error('provider unavailable'))
  await rejected
  await waiting
  assert.equal(session.snapshot().configOptions[0].currentValue, 'auto')
  assert.equal(session.snapshot().status, 'ready')
  const retry = session.setConfigOption('agent-mode', 'plan')
  await tick()
  const next = initial()
  next[0].currentValue = 'plan'
  calls[1].resolve({ configOptions: next, sequence: 3 })
  await retry
  await session.waitForConfiguration()
  assert.equal(session.snapshot().configError, undefined)
  assert.equal(session.snapshot().currentModeId, 'plan')
})

test('prompt barrier waits for every queued change, including changes appended while waiting', async () => {
  const { calls, session } = fixture()
  const first = session.setConfigOption('agent-mode', 'plan')
  let ready = false
  const waiting = session.waitForConfiguration().then(() => { ready = true })
  await tick()
  const second = session.setConfigOption('reasoning', 'low')
  const next = initial()
  next[0].currentValue = 'plan'
  calls[0].resolve({ configOptions: next, sequence: 2 })
  await first
  await tick()
  assert.equal(ready, false)
  assert.equal(calls.length, 2)
  next[2].currentValue = 'low'
  calls[1].resolve({ configOptions: structuredClone(next), sequence: 3 })
  await second
  await waiting
  assert.equal(ready, true)
})

test('same-value mutations do not echo back and newer notifications beat older async response continuations', async () => {
  const { calls, session } = fixture()
  await session.setConfigOption('agent-mode', 'auto')
  assert.equal(calls.length, 0)
  const change = session.setConfigOption('provider-model', 'b/same')
  await tick()
  session.apply({ sessionUpdate: 'config_option_update', configOptions: [] }, 's1', 5)
  calls[0].resolve({ configOptions: initial(), sequence: 4 })
  await change
  assert.deepEqual(session.snapshot().configOptions, [])
  session.applyConfigSnapshot('s1', initial(), 4)
  session.applyConfigSnapshot('other-session', initial(), 10)
  assert.deepEqual(session.snapshot().configOptions, [])
  assert.equal(calls.length, 1)
})

test('early notifications survive adoption; disposing cancels queued work and ignores old-session replies', async () => {
  const { calls, session } = fixture()
  const early = new AgentSession({})
  early.applyConfigSnapshot('s2', [], 9)
  early.adoptSession({ sessionId: 's2', configOptions: initial(), sequence: 8 }, '/new')
  assert.deepEqual(early.snapshot().configOptions, [])
  const first = session.setConfigOption('agent-mode', 'plan')
  const second = session.setConfigOption('reasoning', 'low')
  const failFirst = assert.rejects(first, /Session changed/)
  const failSecond = assert.rejects(second, /Session changed/)
  await tick()
  session.dispose()
  calls[0].resolve({ configOptions: [], sequence: 12 })
  await Promise.all([failFirst, failSecond])
  assert.equal(calls.length, 1)
  assert.equal(session.snapshot().configOptions.length, 3)
  await assert.rejects(session.waitForConfiguration(), /Session changed/)
})

test('transport applies snapshots in wire order before resolving RPC promises (response then notification)', async () => {
  const updates = []
  let session
  const client = new AcpClient('unused', [], '/tmp', {}, {
    onUpdate: (update, id, seq) => { updates.push(seq); session.apply(update, id, seq) },
    onConfigOptions: (id, options, seq) => { updates.push(seq); session.applyConfigSnapshot(id, options, seq) },
    onStderr: () => {}, onExit: () => {}, onRequestPermission: () => {},
  })
  session = new AgentSession(client)
  session.adoptSession({ sessionId: 's1', configOptions: initial(), sequence: 0 }, '/tmp')
  // Exercise the real framing/dispatch code with coalesced stdio messages.
  const result = deferred()
  client.pending.set(7, { method: 'session/set_config_option', sessionId: 's1', resolve: result.resolve, reject: result.reject })
  client.consume(JSON.stringify({ jsonrpc: '2.0', id: 7, result: { configOptions: initial() } }) + '\n' +
    JSON.stringify({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: 's1', update: { sessionUpdate: 'config_option_update', configOptions: [] } } }) + '\n')
  const reply = await result.promise
  session.applyConfigSnapshot('s1', reply.configOptions, reply.sequence)
  assert.deepEqual(updates, [1, 2])
  assert.deepEqual(session.snapshot().configOptions, [])
})

test('transport applies notification then response in arrival order and refuses a v2 handshake', async () => {
  const seen = []
  const client = new AcpClient('unused', [], '/tmp', {}, {
    onUpdate: (_u, _id, seq) => seen.push(seq),
    onConfigOptions: (_id, _options, seq) => seen.push(seq),
    onStderr: () => {}, onExit: () => {}, onRequestPermission: () => {},
  })
  const result = deferred()
  client.pending.set(1, { method: 'session/set_config_option', sessionId: 's1', resolve: result.resolve, reject: result.reject })
  client.consume(JSON.stringify({ method: 'session/update', params: { sessionId: 's1', update: { sessionUpdate: 'config_option_update', configOptions: [] } } }) + '\n')
  client.consume(JSON.stringify({ id: 1, result: { configOptions: initial() } }) + '\n')
  assert.equal((await result.promise).sequence, 2)
  assert.deepEqual(seen, [1, 2])
  client.call = async () => ({ protocolVersion: 2 })
  await assert.rejects(client.initialize(), /Unsupported ACP protocol version/)
})

test('malformed success snapshots reject the change instead of removing confirmed controls', async () => {
  const client = new AcpClient('unused', [], '/tmp', {}, {
    onUpdate: () => {}, onConfigOptions: () => { assert.fail('malformed snapshot published') },
    onStderr: () => {}, onExit: () => {}, onRequestPermission: () => {},
  })
  for (const payload of [{}, { configOptions: null }, { configOptions: {} }]) {
    const result = deferred()
    const rejected = assert.rejects(result.promise, /complete configuration snapshot/)
    client.pending.set(1, { method: 'session/set_config_option', sessionId: 's1', resolve: result.resolve, reject: result.reject })
    client.consume(JSON.stringify({ id: 1, result: payload }) + '\n')
    await rejected
  }
})
