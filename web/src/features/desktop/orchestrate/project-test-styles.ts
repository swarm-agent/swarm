import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { build } from 'vite'
import tailwindcss from '@tailwindcss/vite'

// Render the production layout in browser regressions; unstyled fixtures cannot
// establish pointer hit-testing or modal visibility. No files are emitted.
export async function projectTestStyles(): Promise<string> {
  const styles = await build({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  return outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n') + await readFile('src/features/desktop/orchestrate/swarm-section.css', 'utf8')
}
