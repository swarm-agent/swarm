import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 new session input initializes from defaults without overwriting user changes', async () => {
  const source = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /const \[selectedAgent, setSelectedAgent\] = useState\(agentNameProp\.trim\(\) \|\| agentState\.activePrimary \|\| ''\)/)
  assert.match(source, /const modeManuallySelectedRef = useRef\(false\)/)
  assert.match(source, /const agentManuallySelectedRef = useRef\(false\)/)
  assert.match(source, /const preferenceManuallyChangedRef = useRef\(false\)/)
  assert.match(source, /if \(modeManuallySelectedRef\.current\) return[\s\S]*setMode\(defaultMode\)/)
  assert.match(source, /if \(agentManuallySelectedRef\.current\) return current/)
  assert.match(source, /if \(preferenceManuallyChangedRef\.current\) return current/)
  assert.match(source, /function handleModeChange\(nextMode: DesktopSessionMode\)[\s\S]*modeManuallySelectedRef\.current = true[\s\S]*setMode\(nextMode\)/)
  assert.match(source, /onModeChange=\{handleModeChange\}/)
})
