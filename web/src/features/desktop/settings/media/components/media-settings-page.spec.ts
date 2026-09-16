import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { pricingLabel } from './media-settings-page'

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

test('ModelSelect displays only the model display name on the closed trigger button without pricing', () => {
  assert.match(source, /selectedModel \? selectedModel\.display_name : placeholder/)
  const triggerButtonMatch = source.match(/<button[\s\S]*?ref=\{triggerRef\}[\s\S]*?<\/button>/)
  assert.ok(triggerButtonMatch, 'Trigger button should exist')
  assert.ok(!triggerButtonMatch[0].includes('pricingLabel'), 'Trigger button on the outside must not render pricing')
})

test('ModelSelect dropdown options render stacked rows with model name on top and pricing/metadata on bottom', () => {
  assert.match(source, /Top section: model name/)
  assert.match(source, /Bottom section: meta data for the info/)
  assert.ok(source.includes('option.display_name'), 'Option row must show display name')
  assert.ok(source.includes('pricing || option.model'), 'Option row must show pricing or model metadata')
  assert.ok(source.includes('role="listbox"'), 'Dropdown must have listbox role')
  assert.ok(source.includes('role="option"'), 'Options must have option role')
})

test('pricingLabel formats per-image pricing', () => {
  assert.equal(pricingLabel({ per_image: 0.03 }), '$0.03/image')
  assert.equal(pricingLabel({ per_image: 0.0015 }), '$0.0015/image')
})

test('pricingLabel formats video per-minute pricing', () => {
  assert.equal(pricingLabel({ per_minute: 0.4 }), '$0.4/min')
})

test('pricingLabel formats token input/output/cached pricing', () => {
  assert.equal(
    pricingLabel({ input: 0.075, output: 0.3, cached_input_price_per_million_tokens: 0.01875 }),
    '$0.075 in · $0.3 out · $0.0187 cached / 1M tokens',
  )
})

test('pricingLabel handles free and empty pricing', () => {
  assert.equal(pricingLabel({ is_free: true }), 'Free')
  assert.equal(pricingLabel(null), '')
  assert.equal(pricingLabel(undefined), '')
  assert.equal(pricingLabel({}), '')
})
