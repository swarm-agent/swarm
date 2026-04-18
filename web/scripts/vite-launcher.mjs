#!/usr/bin/env node

import crypto from 'node:crypto'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

if (typeof crypto.hash !== 'function') {
  crypto.hash = (algorithm, data, outputEncoding) =>
    crypto.createHash(algorithm).update(data).digest(outputEncoding)
}
if (typeof globalThis.CustomEvent !== 'function') {
  globalThis.CustomEvent = class CustomEvent extends Event {
    constructor(type, eventInitDict = {}) {
      super(type, eventInitDict)
      this.detail = eventInitDict.detail
    }
  }
}

const originalWarn = console.warn.bind(console)

console.warn = (...args) => {
  const message = args
    .map((value) => (typeof value === 'string' ? value : String(value)))
    .join(' ')

  if (message.includes('Vite requires Node.js version 20.19+ or 22.12+.')) {
    return
  }

  originalWarn(...args)
}

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const viteCliUrl = pathToFileURL(path.resolve(scriptDir, '../node_modules/vite/bin/vite.js'))

const cliArgs = process.argv.slice(2)
if (cliArgs.length === 0) {
  cliArgs.push('dev')
}
process.argv = [process.argv[0], process.argv[1], ...cliArgs]

await import(viteCliUrl.href)
