import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Requirement: PersonalAvatar must use a keyboard-native PNG chooser, render
// only successfully persisted pixels, rehydrate on remount, and discard private
// images on identity reset. A browser plus bounded HTTP fixture is the narrowest
// layer proving picker/DOM behavior; backend tests separately prove durability.
test('personal avatar picker, persistence, rejection and account reset', { timeout: 30000 }, async () => {
  const bundle = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `
    import React from 'react'; import {createRoot} from 'react-dom/client';
    import {PersonalAvatar} from './src/features/desktop/orchestrate/personal-avatar';
    import {ensureDesktopSession} from './src/app/api';
    const root=createRoot(document.getElementById('root'));
    window.mount=(user='user_fixture',account='acct_fixture')=>root.render(<PersonalAvatar key={account+user} userId={user} accountScopeId={account} name="Operator"/>);
    window.resetIdentity=()=>ensureDesktopSession(true); window.mount();
  ` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage()
    const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64')
    let saved = '', writes = 0, fail = false
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (url.pathname === '/v1/auth/desktop/session') return route.fulfill({ json: { ok: true, user_id: 'user_fixture', account_scope_id: 'acct_fixture' } })
      if (url.pathname === '/v1/account/avatar') {
        if (req.method() === 'PUT') {
          writes++
          if (fail) return route.fulfill({ status: 400, json: { error: 'Choose a valid PNG. Try again.' } })
          assert.equal(req.headers()['content-type'], 'image/png')
          assert.deepEqual(req.postDataBuffer(), png)
          saved = `data:image/png;base64,${png.toString('base64')}`
        }
        return route.fulfill({ json: { image: url.searchParams.get('user_id') === 'user_fixture' ? saved : '', user_id: url.searchParams.get('user_id'), account_scope_id: url.searchParams.get('account_scope_id') } })
      }
      return route.fulfill({ status: 404, json: {} })
    })
    const mount = async () => { await page.goto('https://avatar.test'); await page.addScriptTag({ content: bundle.outputFiles[0].text }); await page.getByRole('button', { name: 'Change profile picture' }).waitFor() }
    await mount()
    const button = page.getByRole('button', { name: 'Change profile picture' })
    for (const key of ['Enter', 'Space']) {
      await button.focus()
      const choosing = page.waitForEvent('filechooser')
      await button.press(key)
      const chooser = await choosing
      assert.equal(chooser.isMultiple(), false)
      await chooser.setFiles([])
    }
    assert.equal(writes, 0)
    const input = page.getByLabel('Profile PNG file')
    for (const file of [{ name: 'wrong.jpg', mimeType: 'image/jpeg', buffer: png }, { name: 'big.png', mimeType: 'image/png', buffer: Buffer.alloc(2 * 1024 * 1024 + 1) }]) {
      await input.setInputFiles(file)
      await page.getByRole('alert').waitFor()
      assert.equal(writes, 0)
    }
    await input.setInputFiles({ name: 'profile.png', mimeType: 'image/png', buffer: png })
    await page.getByAltText('Your profile picture').waitFor()
    assert.equal(await page.getByAltText('Your profile picture').getAttribute('src'), saved)
    await mount()
    await page.getByAltText('Your profile picture').waitFor()
    fail = true
    await input.setInputFiles({ name: 'invalid.png', mimeType: 'image/png', buffer: Buffer.from('invalid') })
    await page.getByRole('alert').waitFor()
    assert.equal(await page.getByAltText('Your profile picture').getAttribute('src'), saved)
    await page.evaluate(() => (window as any).resetIdentity())
    assert.equal(await page.getByAltText('Your profile picture').count(), 0)
    await page.evaluate(() => (window as any).mount('second_user', 'second_account'))
    await page.waitForFunction(() => !document.querySelector('button')?.disabled)
    assert.equal(await page.getByAltText('Your profile picture').count(), 0)
  } finally { await browser.close() }
})
