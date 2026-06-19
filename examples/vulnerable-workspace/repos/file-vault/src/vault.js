'use strict'

const fs = require('node:fs')
const http = require('node:http')
const path = require('node:path')

function resolveDocumentPath(vaultRoot, requestedName) {
  if (typeof requestedName !== 'string' || requestedName.length === 0) {
    throw new Error('document name is required')
  }

  const normalizedName = path.normalize(requestedName)
  return path.join(vaultRoot, normalizedName)
}

function readDocument(vaultRoot, requestedName) {
  const documentPath = resolveDocumentPath(vaultRoot, requestedName)
  return fs.readFileSync(documentPath, 'utf8')
}

function createServer(vaultRoot) {
  return http.createServer((request, response) => {
    const requestURL = new URL(request.url, 'http://127.0.0.1')
    if (request.method !== 'GET' || requestURL.pathname !== '/documents') {
      response.writeHead(404, { 'content-type': 'application/json' })
      response.end(JSON.stringify({ error: 'not found' }))
      return
    }

    const requestedName = requestURL.searchParams.get('name') || ''
    try {
      const body = readDocument(vaultRoot, requestedName)
      response.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' })
      response.end(body)
    } catch (error) {
      response.writeHead(404, { 'content-type': 'application/json' })
      response.end(JSON.stringify({ error: error.message }))
    }
  })
}

module.exports = {
  createServer,
  readDocument,
  resolveDocumentPath
}
