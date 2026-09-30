import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium, type Page } from 'playwright'
import path from 'node:path'

// Requirement: every sidebar row shares the scrollport's centered content lane,
// independent of viewport width, scrollbar presence, tool kind or disclosure.
// Threat: compensating Bash translations, intrinsic flex widths and assistant
// caps make cards escape or hide actionable controls on phones/narrow panels.
// Authority: DesktopV3ChatContentLane -> DesktopV3RenderItemView -> ChatMarkdown /
// ToolMessageView / SearchReadToolGroupView, plus the real Bash permission card.
// Layer: hermetic React browser fixture with compiled production theme/Tailwind;
// geometry and pointer callbacks prove layout, not source text or live providers.
// The second mode uses the production sticky-bottom hook with react-virtual's
// measured absolute rows, proving resize remeasurement and retained tail follow.
async function assertLane(page: Page, centered: boolean) {
  const failures = await page.getByTestId('desktop-chat-content-lane').evaluate((lane, centered) => {
    const failures: string[] = []
    const box = lane.getBoundingClientRect()
    const style = getComputedStyle(lane)
    const left = box.left + parseFloat(style.paddingLeft)
    const right = box.right - parseFloat(style.paddingRight)
    for (const row of lane.querySelectorAll('[data-fixture-row]')) {
      const rect = row.getBoundingClientRect()
      if (rect.left < left - 2 || rect.right > right + 2) failures.push('row escaped')
      for (const card of row.querySelectorAll('[data-chat-tool-message], [data-search-read-group], [data-chat-assistant-body], [data-permission-fixture] > section')) {
        const r = card.getBoundingClientRect()
        if (r.left < left - 2 || r.right > right + (centered ? 2 : 7)) failures.push('card escaped')
        if (centered && Math.abs((r.left - left) - (right - r.right)) > 2) failures.push('unequal card gutters')
      }
      for (const button of row.querySelectorAll('button')) {
        const r = button.getBoundingClientRect()
        if (r.width && (r.left < left - 2 || r.right > right + (centered ? 2 : 7))) failures.push('action escaped')
      }
    }
    if (lane.scrollWidth > lane.clientWidth + 2) failures.push('lane overflow')
    return failures
  }, centered)
  assert.deepEqual(failures, [])
}

test('sidebar cards share a scrollport-relative lane and preserve page presentation', { timeout: 60_000 }, async () => {
  const long = 'unbroken-path-name-'.repeat(28)
  const code = `\`\`\`text\n${long}\n| column | value |\n| ------ | ----- |\n| path | ${long} |\n\`\`\``
  const markdown = `https://example.invalid/${long}\n\n${code}\n\n<copy label="Exact command">${long}</copy>`
  const bundle = await build({
    stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
      import React,{useState} from 'react'; import {createRoot} from 'react-dom/client';
      import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
      import {useVirtualizer} from '@tanstack/react-virtual';
      import {DesktopV3ChatContentLane,DesktopV3RenderItemView,useDesktopV3StickyBottomScroll} from './src/features/desktop/chat/components/desktop-v3-existing-conversation-pane';
      import {DesktopInlineBashPermissionCard} from './src/features/desktop/chat/components/desktop-inline-bash-permission-card';
      import {buildStructuredToolMessage} from './src/features/desktop/chat/services/tool-message';
      const long=${JSON.stringify(long)}, markdown=${JSON.stringify(markdown)};
      const tool=(name,args={},output='result',state='done',error='')=>buildStructuredToolMessage({tool:name,callId:name,argumentsText:JSON.stringify(args),outputText:typeof output==='string'?output:JSON.stringify(output),state,error});
      const message=(id,toolMessage,content='')=>({type:'message',message:{id,session_id:'fixture',global_seq:1,role:toolMessage?'tool':'assistant',content,toolMessage}});
      const bash=tool('bash',{command:'printf '+long,explanation:'Read the requested output.',category:'read',critical:false},Array.from({length:240},(_,i)=>'line '+i+' '+long).join('\\n'));
      const read=tool('read',{path:long},{path:long,lines:[{line:1,text:long}],line_start:1,total_lines:1});
      const search=tool('search',{query:long},{results:[{path:long,items:[{line:1,text:long}]}]});
      const task=tool('task',{}, {launches:[{launch_index:1,child_session_id:'child',subagent:'coder',assignment_label:long,status:'done'}]});
      const program={...task,taskProgram:{id:'program',state:'done',activeStageId:'stage',nextAction:'review',stages:[{id:'stage',dependsOn:[],dependencyEvidence:long,state:'done',rows:task.taskRows}]}};
      const plan=tool('plan_manage',{action:'complete_subtask',checkpoint_id:'cp',subtask_id:'sub'}, {action:'complete_subtask',status:'ok',checkpoint_id:'cp',plan:{title:long,document:{checkpoints:[{id:'cp',title:long,status:'in_progress',subtasks:[{id:'sub',title:long,status:'completed'}]}]}}});
      const items=[message('bash',bash),{type:'search-read-group',id:'group',toolMessages:[read,search]},message('generic',tool('custom-'+long,{},long)),message('task',task),message('program',program),message('plan',plan),message('loading',tool('read',{},'','running')),message('error',tool('read',{},'','error',long)),message('assistant',null,markdown)];
      const permission={id:'permission',toolName:'bash',toolArguments:JSON.stringify({command:'cat '+long,explanation:'Read '+long,category:'read',critical:false}),mode:'auto',status:'pending'};
      window.calls=[];
      function App(){
        const [presentation,setPresentation]=useState('sidebar'),[virtual,setVirtual]=useState(false);
        window.mode=(presentation,virtual=false)=>{setPresentation(presentation);setVirtual(virtual)};
        const sticky=useDesktopV3StickyBottomScroll({resetKey:String(virtual),itemCount:items.length,followKey:String(virtual)});
        const v=useVirtualizer({count:virtual?items.length:0,getScrollElement:()=>sticky.scrollContainerRef.current,estimateSize:()=>180,overscan:12});
        const render=(item,index)=><DesktopV3RenderItemView item={item} index={index} thinkingTagsEnabled taskChildActions={{workspaceSlug:'fixture',parentSessionId:'fixture',onNavigate:()=>window.calls.push('open')}}/>;
        return <div ref={sticky.scrollContainerRef} data-testid="fixture-scrollport" className="h-full min-h-0 overflow-y-auto [scrollbar-gutter:stable_both-edges]">
          <DesktopV3ChatContentLane contentRef={sticky.contentRef} presentation={presentation}>
            {virtual ? <div className="relative min-w-0 shrink-0" style={{height:v.getTotalSize()}}>{v.getVirtualItems().map(row=><div key={row.key} ref={v.measureElement} data-index={row.index} data-fixture-row className="absolute left-0 top-0 w-full min-w-0" style={{transform:'translateY('+row.start+'px)',paddingBottom:20}}>{render(items[row.index],row.index)}</div>)}</div> : items.map((item,i)=><div key={i} data-fixture-row>{render(item,i)}</div>)}
            {!virtual && <div data-fixture-row data-permission-fixture><DesktopInlineBashPermissionCard permission={permission} pendingCount={1} sessionMode="auto" onResolve={async (_,action)=>{window.calls.push(action)}} onOpenPermissions={()=>window.calls.push('settings')}/></div>}
            <div data-testid="fixture-tail" className="h-px shrink-0"/>
          </DesktopV3ChatContentLane>
        </div>
      }
      const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
      createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><App/></QueryClientProvider>);
    ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent',
  })
  const styles = await buildStyles({ configFile: false, publicDir: false, logLevel: 'silent', plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, ...(process.env.SWARM_TEST_BROWSER_CHANNEL ? { channel: process.env.SWARM_TEST_BROWSER_CHANNEL } : {}) })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    page.setDefaultTimeout(5_000)
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => new URL(route.request().url()).pathname === '/'
      ? route.fulfill({ contentType: 'text/html', body: '<div id="root" style="width:320px;height:600px"></div>' })
      : route.fulfill({ json: { explain: { rule_preview: 'allow bash prefix: cat', bash_profile: 'read_only' } } }))
    await page.goto('https://lane.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0]!.text })
    const lane = page.getByTestId('desktop-chat-content-lane')
    await lane.waitFor()
    for (const [viewport, width] of [[390, 320], [1440, 320], [1440, 480], [1440, 900], [1600, 1400]]) {
      await page.setViewportSize({ width: viewport, height: 900 })
      await page.locator('#root').evaluate((root, width) => { root.style.width = `${width}px` }, width)
      await assertLane(page, true)
      assert.equal(await lane.evaluate(node => getComputedStyle(node).paddingLeft), '16px')
      const expand = page.locator('[data-chat-tool-message]').first().getByRole('button', { expanded: false })
      await expand.click()
      await page.locator('[data-bash-output="virtualized"]').waitFor()
      await assertLane(page, true)
      await page.locator('[data-chat-tool-message]').first().getByRole('button', { expanded: true }).click()
      await page.locator('[data-bash-output="bounded-preview"]').waitFor()
    }
    await page.locator('#root').evaluate(root => { root.style.width = '320px'; root.style.setProperty('--swarm-chat-gutter', '20px') })
    assert.equal(await lane.evaluate(node => getComputedStyle(node).paddingLeft), '20px')
    await assertLane(page, true)
    const symmetry = await lane.evaluate(node => {
      const lane = node.getBoundingClientRect(), port = node.parentElement!.getBoundingClientRect()
      return Math.abs((lane.left - port.left) - (port.right - lane.right))
    })
    assert.ok(symmetry <= 2, 'content frame is centered inside the actual scrollport')
    const pre = lane.locator('.chat-markdown pre').first()
    assert.ok(await pre.evaluate(node => { node.scrollLeft = node.scrollWidth; return node.scrollWidth > node.clientWidth && node.scrollLeft > 0 }), 'wide code/table-shaped output scrolls locally')
    await lane.getByRole('button', { name: 'Copy', exact: true }).last().click()
    await lane.getByRole('button', { name: 'Deny', exact: true }).click()
    assert.deepEqual(await page.evaluate(() => (window as any).calls), ['deny'])
    // Page regression: desktop padding and the legacy page Bash offset are unchanged.
    await page.evaluate(() => (window as any).mode('page'))
    await page.waitForFunction(() => document.querySelector('[data-chat-presentation]')?.getAttribute('data-chat-presentation') === 'page')
    await page.locator('#root').evaluate(root => { root.style.width = '1100px' })
    await assertLane(page, false)
    assert.equal(await lane.evaluate(node => getComputedStyle(node).paddingLeft), '48px')
    assert.equal(await lane.locator('[data-chat-tool-message]').first().evaluate(node => getComputedStyle(node).translate), '5px')
    // Real resize observers + measured virtual rows must retain the tail after resize.
    await page.evaluate(() => (window as any).mode('sidebar', true))
    await page.waitForFunction(() => document.querySelector('[data-fixture-row][data-index]') && document.querySelector('[data-chat-presentation]')?.getAttribute('data-chat-presentation') === 'sidebar')
    for (const width of [320, 900, 320, 900]) {
      await page.locator('#root').evaluate((root, width) => { root.style.width = `${width}px` }, width)
      await page.waitForFunction(() => { const s = document.querySelector('[data-testid="fixture-scrollport"]')!; return s.scrollHeight - s.scrollTop - s.clientHeight <= 2 })
      await assertLane(page, true)
      const overlap = await lane.evaluate(node => {
        const rows = [...node.querySelectorAll('[data-fixture-row]')].map(row => row.getBoundingClientRect())
        return rows.some((row, i) => i > 0 && row.top < rows[i - 1].bottom - 2)
      })
      assert.equal(overlap, false, 'virtual rows are remeasured, not overlapping after resize')
    }
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
