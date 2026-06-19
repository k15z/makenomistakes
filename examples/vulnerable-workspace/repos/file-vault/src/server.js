'use strict'

const path = require('node:path')
const { createServer } = require('./vault')

const vaultRoot = path.resolve(process.argv[2] || path.join(__dirname, '..', 'vault'))
const port = Number.parseInt(process.env.PORT || '3000', 10)

const server = createServer(vaultRoot)
server.listen(port, '127.0.0.1', () => {
  process.stdout.write(`file-vault listening on http://127.0.0.1:${port}\n`)
  process.stdout.write(`vault root: ${vaultRoot}\n`)
})
