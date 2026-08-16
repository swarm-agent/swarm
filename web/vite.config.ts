import { createHash } from 'node:crypto'
import { copyFile, mkdir, readdir, readFile, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import path from 'node:path'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const backendTarget = process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781'
const desktopPort = Number(process.env.SWARM_DESKTOP_PORT || '5555')
const serviceWorkerBuildIDToken = '__SWARM_PWA_BUILD_ID__'

const packageRequire = createRequire(import.meta.url)
function packageRoot(entrypoint: string, packageName: string): string {
  const marker = `${path.sep}node_modules${path.sep}${packageName.split('/').join(path.sep)}${path.sep}`
  const markerIndex = entrypoint.lastIndexOf(marker)
  if (markerIndex < 0) throw new Error(`Cannot resolve reviewed animation runtime package root for ${packageName}`)
  return entrypoint.slice(0, markerIndex + marker.length - 1)
}

function animationRuntimeAssets() {
  const threeRoot = packageRoot(packageRequire.resolve('three'), 'three')
  const dotLottieRoot = packageRoot(packageRequire.resolve('@lottiefiles/dotlottie-web'), '@lottiefiles/dotlottie-web')
  const riveRoot = packageRoot(packageRequire.resolve('@rive-app/canvas'), '@rive-app/canvas')
  return [
    [path.join(threeRoot, 'build', 'three.module.js'), 'three.module.js'],
    [path.join(threeRoot, 'build', 'three.core.js'), 'three.core.js'],
    [path.join(dotLottieRoot, 'dist', 'index.js'), 'dotlottie.js'],
    [path.join(dotLottieRoot, 'dist', 'dotlottie-player.wasm'), 'dotlottie-player.wasm'],
    [path.join(riveRoot, 'rive.js'), 'rive.js'],
    [path.join(riveRoot, 'rive.wasm'), 'rive.wasm'],
    [path.join(riveRoot, 'rive_fallback.wasm'), 'rive_fallback.wasm'],
  ] as const
}

function bundleAnimationRuntimes(): Plugin {
  let outputDirectory = ''
  return {
    name: 'bundle-animation-runtimes',
    configResolved(config) {
      outputDirectory = path.resolve(config.root, config.build.outDir, 'swarm-animation-runtime')
    },
    async writeBundle() {
      await mkdir(outputDirectory, { recursive: true })
      await Promise.all([
        ...animationRuntimeAssets().map(([source, target]) => copyFile(source, path.join(outputDirectory, target))),
        copyFile(path.resolve(import.meta.dirname, '..', 'THIRD_PARTY_NOTICES.md'), path.join(outputDirectory, 'THIRD_PARTY_NOTICES.md')),
      ])
    },
    configureServer(server) {
      server.middlewares.use('/swarm-animation-runtime', (request, response, next) => {
        const requestPath = request.url?.split('?', 1)[0] ?? ''
        if (requestPath !== `/${path.posix.basename(requestPath)}`) return next()
        const filename = path.posix.basename(requestPath)
        const asset = animationRuntimeAssets().find(([, target]) => target === filename)
        if (!asset) return next()
        response.setHeader('Cache-Control', 'no-store')
        response.setHeader('Access-Control-Allow-Origin', '*')
        response.setHeader('Cross-Origin-Resource-Policy', 'cross-origin')
        response.setHeader('X-Content-Type-Options', 'nosniff')
        response.setHeader('Content-Type', filename.endsWith('.wasm') ? 'application/wasm' : 'text/javascript; charset=utf-8')
        void readFile(asset[0]).then((bytes) => response.end(bytes), next)
      })
    },
  }
}

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
  plugins: [react(), tailwindcss(), bundleAnimationRuntimes(), versionBuiltServiceWorker()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'desktop',
              test: /src[\\/]features[\\/]desktop[\\/]/,
              maxSize: 400 * 1024,
            },
          ],
        },
      },
    },
  },
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
      '/v2': {
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
