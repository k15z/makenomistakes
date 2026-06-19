'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')

const { status } = require('../src/status')

test('returns a stable health payload', () => {
  const result = status(new Date('2026-01-01T00:00:00Z'))

  assert.deepEqual(result, {
    ok: true,
    service: 'health-check',
    checked_at: '2026-01-01T00:00:00.000Z'
  })
})
