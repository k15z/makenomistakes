'use strict'

function status(now = new Date()) {
  return {
    ok: true,
    service: 'health-check',
    checked_at: now.toISOString()
  }
}

module.exports = { status }
