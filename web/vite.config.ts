import { createHash } from 'node:crypto'
import { readdir, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const backendTarget = process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781'
const desktopPort = Number(process.env.SWARM_DESKTOP_PORT || '5555')
const serviceWorkerBuildIDToken = '__SWARM_PWA_BUILD_ID__'

function versionBuiltServiceWorker(): Plugin {
  let outputDirectory = 'dist'

  return {
    name: 'version-built-service-worker',
    apply: 'build',
    configResolved(config) {
      outputDirectory = path.resolve(config.root, config.build.outDir)
    },
    async closeBundle() {
      const outputFiles = await listOutputFiles(outputDirectory)
      const hashedFiles = outputFiles.filter((file) => path.relative(outputDirectory, file) !== 'sw.js')
      if (hashedFiles.length === 0) {
        throw new Error('Cannot version service worker: Vite produced no hashable output files')
      }

      const digest = createHash('sha256')
      for (const file of hashedFiles) {
        const relativePath = path.relative(outputDirectory, file).split(path.sep).join('/')
        digest.update(relativePath)
        digest.update('\0')
        digest.update(await readFile(file))
        digest.update('\0')
      }

      const serviceWorkerPath = path.join(outputDirectory, 'sw.js')
      const serviceWorker = await readFile(serviceWorkerPath, 'utf8')
      const tokenOccurrences = serviceWorker.split(serviceWorkerBuildIDToken).length - 1
      if (tokenOccurrences !== 1) {
        throw new Error(`Cannot version service worker: expected exactly one ${serviceWorkerBuildIDToken} token, found ${tokenOccurrences}`)
      }

      const buildID = digest.digest('hex').slice(0, 20)
      await writeFile(serviceWorkerPath, serviceWorker.replace(serviceWorkerBuildIDToken, buildID))
    },
  }
}

async function listOutputFiles(directory: string): Promise<string[]> {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = await Promise.all(entries.map(async (entry) => {
    const entryPath = path.join(directory, entry.name)
    return entry.isDirectory() ? listOutputFiles(entryPath) : [entryPath]
  }))
  return files.flat().sort()
}

export default defineConfig({
  plugins: [react(), tailwindcss(), versionBuiltServiceWorker()],
  server: {
    host: '127.0.0.1',
    port: Number.isFinite(desktopPort) ? desktopPort : 5555,
    strictPort: true,
    proxy: {
      '/v1': {
        target: backendTarget,
        changeOrigin: false,
        ws: true,
      },
      '/v3': {
        target: backendTarget,
        changeOrigin: false,
        ws: true,
      },
      '/healthz': backendTarget,
      '/readyz': backendTarget,
      '/desktop': backendTarget,
      '/ws': {
        target: backendTarget.replace(/^http/i, 'ws'),
        changeOrigin: false,
        ws: true,
      },
    },
  },
})
