'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const test = require('node:test')
const assert = require('node:assert/strict')

const { readDocument, resolveDocumentPath } = require('../src/vault')

test('reads documents from the configured vault directory', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'file-vault-test-'))
  fs.writeFileSync(path.join(dir, 'welcome.txt'), 'hello from the vault\n')

  assert.equal(readDocument(dir, 'welcome.txt'), 'hello from the vault\n')
})

test('requires a document name', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'file-vault-test-'))

  assert.throws(() => readDocument(dir, ''), /document name is required/)
})

test('normalizes simple relative document paths', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'file-vault-test-'))

  assert.equal(resolveDocumentPath(dir, './welcome.txt'), path.join(dir, 'welcome.txt'))
})
