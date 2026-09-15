import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./media-settings-page.tsx', import.meta.url), 'utf8')

test('Media settings page places source media folder at the very top', () => {
  const sourceFolderIndex = source.indexOf('aria-labelledby="source-media-title"')
  const modelsIndex = source.indexOf('aria-labelledby="media-models-title"')
  const transcribeIndex = source.indexOf('aria-labelledby="transcribe-video-title"')

  assert.ok(sourceFolderIndex > 0, 'Source media folder section should exist')
  assert.ok(modelsIndex > sourceFolderIndex, 'Media models section should appear below source media folder')
  assert.ok(transcribeIndex > modelsIndex, 'Transcribe videos section should appear below media models')
})

test('Media models section consolidates all four model types into one unified section', () => {
  const modelsSection = source.slice(
    source.indexOf('aria-labelledby="media-models-title"'),
    source.indexOf('aria-labelledby="transcribe-video-title"')
  )

  assert.ok(modelsSection.includes('aria-labelledby="image-model-title"'), 'Image generation model should be inside models section')
  assert.ok(modelsSection.includes('aria-labelledby="video-generation-title"'), 'Base video generation model should be inside models section')
  assert.ok(modelsSection.includes('aria-labelledby="video-iteration-title"'), 'Video iteration model should be inside models section')
  assert.ok(modelsSection.includes('aria-labelledby="transcription-model-title"'), 'Video understanding model should be inside models section')
})

test('Media models section contains clear explanation of how media models work', () => {
  const modelsSection = source.slice(
    source.indexOf('aria-labelledby="media-models-title"'),
    source.indexOf('aria-labelledby="transcribe-video-title"')
  )

  assert.ok(modelsSection.includes('How media models work:'), 'Explanation banner should be present')
  assert.ok(modelsSection.includes('Selected default models are used automatically'), 'Explanation text should be descriptive')
})

test('Video transcription section follows models section in condensed layout', () => {
  assert.match(source, /id="transcribe-video-title"/)
  assert.match(source, /Select designated folder/)
  assert.match(source, /Analyze and transcribe/)
})
