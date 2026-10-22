import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ConfigSelectors } from '../dist/test-renderer/renderer/components/ConfigSelectors.js'

const option = (id, category) => ({
  id, category, name: `Agent ${id}`, type: 'select', currentValue: 'one',
  options: [{ value: 'one', name: 'Agent label one' }, { value: 'two', name: 'Agent label two' }],
})
const render = (options, extra = {}) => renderToStaticMarkup(createElement(ConfigSelectors, {
  snapshot: { status: 'ready', configOptions: options, ...extra },
  onChange: async () => {},
}))

test('three Agent controls are adjacent; missing and unsupported controls are not fabricated', () => {
  const controls = [option('opaque-mode', 'mode'), option('opaque-model', 'model'), option('opaque-thought', 'thought_level')]
  const html = render(controls)
  assert.equal((html.match(/<select/g) ?? []).length, 3)
  assert.ok(html.indexOf('Agent opaque-mode') < html.indexOf('Agent opaque-model'))
  assert.ok(html.indexOf('Agent opaque-model') < html.indexOf('Agent opaque-thought'))
  assert.ok(!html.includes('More settings'))
  assert.equal((render(controls.slice(0, 2)).match(/<select/g) ?? []).length, 2)
  assert.equal(render(undefined), '')
  assert.ok(!render([]).includes('<select'))
})

test('thinking-only options render independently and unknown/duplicate categories use generic settings', () => {
  const reasoning = render([option('reasoning', 'thought_level')])
  assert.equal((reasoning.match(/<select/g) ?? []).length, 1)
  assert.ok(!reasoning.includes('More settings'))
  const html = render([option('first-model', 'model'), option('second-model', 'model'), option('custom', 'unknown')])
  assert.ok(html.includes('<details'))
  assert.ok(html.indexOf('Agent first-model') < html.indexOf('<details'))
  assert.ok(html.indexOf('Agent second-model') > html.indexOf('<details'))
  assert.ok(html.indexOf('Agent custom') > html.indexOf('<details'))
})

test('group labels and Agent-confirmed values survive rendering while pending or generating', () => {
  const grouped = {
    ...option('model', 'model'),
    options: [{ group: 'provider', name: 'Agent Provider', options: [{ value: 'one', name: 'Agent Model' }] }],
  }
  const html = render([grouped], { status: 'prompting', configPending: 2, configError: 'Could not apply settings' })
  assert.ok(html.includes('optgroup label="Agent Provider"'))
  assert.ok(html.includes('value="one" selected=""'))
  assert.ok(html.includes('Applying settings…'))
  assert.ok(html.includes('Could not apply settings'))
  assert.ok(!html.includes('disabled=""'))
  assert.ok(render([grouped], { status: 'exited' }).includes('disabled=""'))
})
