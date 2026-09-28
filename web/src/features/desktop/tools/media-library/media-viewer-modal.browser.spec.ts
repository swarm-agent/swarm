import assert from 'node:assert/strict'
import test from 'node:test'
import { build } from 'esbuild'
import { chromium } from 'playwright'

// Written purpose:
// Requirement: MediaViewerModal must enforce capability-based preflight gates and provenance integrity:
// 1. Veo Lite must disable submission and not invoke onGenerate callback when Next Scene/Fine Tune is invalid.
// 2. Veo Standard/Fast must enforce 8s duration, 720p resolution, and source aspect ratio matching.
// 3. Gemini Omni must use automatic duration (supportsDuration=false, no manual duration selection).
// 4. Unavailable image models must be preserved in the selector rather than silently falling back to default.
// 5. Changing source item / navigation / reopening resets and updates eligibility without clobbering user choices.
// 6. onGenerate spy verifies exact payloads and ensures invalid actions never fire callbacks.
// Threat/regression: Opening an existing video (like Veo Lite) defaulted to iteration model (Omni),
// silently mutating model and generating invalid requests.
// Authority: MediaViewerModal, validateMediaGenerationRequest, evaluateVideoActionSupport.

test('MediaViewerModal rendered component behavior with onGenerate spy', { timeout: 30000 }, async () => {
  const fixture = `
    import React from 'react';
    import { createRoot } from 'react-dom/client';
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
    import { MediaViewerModal } from './src/features/desktop/tools/media-library/media-viewer-modal';

    window.calls = [];
    window.onGenerateSpy = async (req) => {
      window.calls.push(req);
    };

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    const root = createRoot(document.getElementById('root'));

    window.renderViewer = (item, initialMode) => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MediaViewerModal
            key={item ? item.id : 'none'}
            item={item}
            items={item ? [item] : []}
            onClose={() => {}}
            onSelect={() => {}}
            onGenerate={window.onGenerateSpy}
            initialQuickRouteMode={initialMode}
          />
        </QueryClientProvider>
      );
    };
  `;

  const catalogMock = {
    image_models: [
      { id: 'imagen-3.0', provider: 'google', model: 'imagen-3.0', display_name: 'Imagen 3.0', kind: 'image_generation', ready: true, generation_options: { aspect_ratios: ['1:1', '16:9'], default_ratio: '1:1', resolutions: ['1k'], default_resolution: '1k' } },
    ],
    video_generation_models: [
      {
        id: 'veo-3.1-generate-preview',
        provider: 'google',
        model: 'veo-3.1-generate-preview',
        display_name: 'Veo 3.1 Standard',
        kind: 'video_generation',
        ready: true,
        generation_options: {
          aspect_ratios: ['16:9', '9:16'],
          resolutions: ['720p', '1080p'],
          durations: [4, 6, 8],
          default_ratio: '16:9',
          default_resolution: '720p',
          default_duration: 8,
        },
        constraints: {
          model: 'veo-3.1-generate-preview',
          provider: 'google',
          create: { supported: true },
          edit: { supported: false, reason: 'Veo models do not support video editing' },
          extend: {
            supported: true,
            locked_duration_seconds: 8,
            locked_resolution: '720p',
            locked_aspect_ratio_matches_source: true,
            supported_aspect_ratios: ['16:9', '9:16'],
            max_extension_count: 20,
            requires_veo_source: true,
            disallows_veo_lite_source: true,
            requires_source_provenance: true,
            required_source_provider: 'google',
            required_source_transport: 'google_predict_long_running',
            requires_provider_resource: true,
            requires_output_digest: true,
            requires_known_extension_count: true,
            allowed_source_models: ['veo-3.1-generate-preview', 'veo-3.1-fast-generate-preview'],
            disallowed_source_models: ['veo-3.1-lite-generate-preview'],
            observed_dimension_pairs: [[1280, 720], [720, 1280]],
          },
        },
      },
      {
        id: 'veo-3.1-lite-generate-preview',
        provider: 'google',
        model: 'veo-3.1-lite-generate-preview',
        display_name: 'Veo 3.1 Lite',
        kind: 'video_generation',
        ready: true,
        generation_options: {
          aspect_ratios: ['16:9', '9:16'],
          resolutions: ['720p'],
          durations: [4, 6, 8],
          default_ratio: '16:9',
          default_resolution: '720p',
          default_duration: 8,
        },
        constraints: {
          model: 'veo-3.1-lite-generate-preview',
          provider: 'google',
          create: { supported: true },
          edit: { supported: false, reason: 'Veo models do not support video editing' },
          extend: { supported: false, reason: 'Veo Lite does not support next scene extension; use Veo 3.1 standard or fast.' },
        },
      },
      {
        id: 'gemini-omni-1.1-flash',
        provider: 'google',
        model: 'gemini-omni-1.1-flash',
        display_name: 'Gemini Omni 1.1 Flash',
        kind: 'video_iteration',
        ready: true,
        generation_options: {
          aspect_ratios: ['16:9', '9:16'],
          resolutions: ['720p'],
          default_ratio: '16:9',
          default_resolution: '720p',
        },
        constraints: {
          model: 'gemini-omni-1.1-flash',
          provider: 'google',
          create: { supported: true, supports_duration: false },
          edit: {
            supported: true,
            supports_duration: false,
            max_external_duration_sec: 10.0,
            requires_handle_match: true,
            supported_providers: ['google'],
            required_source_provider: 'google',
            required_source_transport: 'google_interactions',
            source_model_match: 'gemini-omni-1.1-flash',
          },
          extend: {
            supported: true,
            supports_duration: false,
            max_source_duration_sec: 37.0,
            max_total_duration_sec: 40.0,
            requires_omni_source: true,
            required_source_provider: 'google',
            required_source_transport: 'google_interactions',
            requires_interaction_handle: true,
            requires_known_extension_count: true,
            allowed_source_models: ['gemini-omni-1.1-flash'],
          },
        },
      },
    ],
    video_iteration_models: [
      {
        id: 'gemini-omni-1.1-flash',
        provider: 'google',
        model: 'gemini-omni-1.1-flash',
        display_name: 'Gemini Omni 1.1 Flash',
        kind: 'video_iteration',
        ready: true,
      },
    ],
  };

  const bundle = await build({
    stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: fixture },
    bundle: true,
    write: false,
    platform: 'browser',
    format: 'iife',
    jsx: 'automatic',
    logLevel: 'silent',
    plugins: [
      {
        name: 'mock-boundaries',
        setup(b) {
          b.onResolve({ filter: /(?:app\/api|get-media-settings|uiSettingsQueryOptions)$/ }, (args) => ({
            path: args.path.split('/').pop()!,
            namespace: 'fixture',
          }));
          b.onLoad({ filter: /.*/, namespace: 'fixture' }, (args) => {
            if (args.path === 'get-media-settings') {
              return {
                loader: 'tsx',
                contents: `export const getMediaSettingsCatalog = async () => (${JSON.stringify(catalogMock)});`,
              };
            }
            if (args.path === 'uiSettingsQueryOptions') {
              return {
                loader: 'tsx',
                contents: `export const uiSettingsQueryOptions = () => ({ queryKey: ['ui-settings'], queryFn: async () => ({}) });`,
              };
            }
            return {
              loader: 'tsx',
              contents: `export const requestJson = async () => ({});`,
            };
          });
        },
      },
    ],
  });

  const browser = await chromium.launch({
    headless: true,
    channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome',
  });

  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(10000);
    await page.route('**/*', (route) =>
      route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }),
    );
    await page.goto('https://viewer.test/');
    await page.addScriptTag({ content: bundle.outputFiles[0].text });

    // 1. Veo Lite item: Next scene extension must be disabled, no callback on click
    const veoLiteItem = {
      id: 'item-veo-lite',
      title: 'Veo Lite Video',
      filename: 'lite.mp4',
      mediaType: 'video/mp4',
      kind: 'video',
      createdAt: 1000,
      formattedDate: 'Sep 20',
      formattedTime: '10:00 AM',
      dayKey: '2026-09-20',
      dayLabel: 'Today',
      sessionId: 'sess-1',
      sessionTitle: 'Session',
      workspacePath: '/ws',
      workspaceName: 'demo',
      model: 'veo-3.1-lite-generate-preview',
      aspectRatio: '16:9',
      resolution: '720p',
      durationSeconds: 8,
      directUrl: '/v3/sessions/sess-1/artifacts/art-lite',
      artifact: {
        artifactId: 'art-lite',
        sessionId: 'sess-1',
        label: 'Lite',
        filename: 'lite.mp4',
        kind: 'video',
        mediaType: 'video/mp4',
      },
      videoProvenance: {
        account_scope_id: 'acc-1',
        provider: 'google',
        model: 'veo-3.1-lite-generate-preview',
        transport: 'google_predict_long_running',
        operation: 'create',
        created_at: 1000,
        expires_at: 1000 + 48 * 3600 * 1000,
        observed_width: 1280,
        observed_height: 720,
        observed_duration_ms: 8000,
        aspect_ratio: '16:9',
        resolution: '720p',
      },
    };

    await page.evaluate((item) => (window as any).renderViewer(item, 'next_scene'), veoLiteItem);

    // Wait for viewer dialog
    await page.getByRole('dialog', { name: /Media viewer: Veo Lite Video/i }).waitFor();

    // Fill in a prompt
    const promptInput = page.locator('textarea');
    await promptInput.fill('Extend next sequence');

    // Submit button should be disabled because Veo Lite extension is unsupported
    const submitBtn = page.getByRole('button', { name: /Generate revision/i });
    const isDisabled = await submitBtn.isDisabled();
    assert.equal(isDisabled, true, 'Submit button must be disabled for Veo Lite extension');

    // Clicking disabled button must not produce any calls
    await submitBtn.click({ force: true });
    let calls = await page.evaluate(() => (window as any).calls);
    assert.equal(calls.length, 0, 'onGenerate spy must not be invoked for unsupported Veo Lite');

    // 2. Veo 3.1 Standard item: Next scene supported, locks duration=8s, resolution=720p, AR=16:9
    const veoStdItem = {
      ...veoLiteItem,
      id: 'item-veo-std',
      title: 'Veo Standard Video',
      model: 'veo-3.1-generate-preview',
      artifact: { ...veoLiteItem.artifact, artifactId: 'art-std' },
      videoProvenance: {
        ...veoLiteItem.videoProvenance,
        model: 'veo-3.1-generate-preview',
        extension_count: 0,
        extension_count_known: true,
        has_provider_resource: true,
        output_digest_sha256: 'a'.repeat(64),
      },
    };

    await page.evaluate((item) => (window as any).renderViewer(item, 'next_scene'), veoStdItem);
    await page.getByRole('dialog', { name: /Media viewer: Veo Standard Video/i }).waitFor();

    // Check locked duration selector: disabled and displays 8s
    const durSelect = page.locator('select[aria-label="Video Duration"]');
    assert.equal(await durSelect.isDisabled(), true, 'Duration select must be disabled / locked for Veo extension');
    assert.equal(await durSelect.inputValue(), '8', 'Duration must be locked to 8s');

    // Check locked resolution: disabled and displays 720p
    const resSelect = page.locator('select[aria-label="Resolution"]');
    assert.equal(await resSelect.isDisabled(), true, 'Resolution select must be disabled / locked for Veo extension');
    assert.equal(await resSelect.inputValue(), '720p', 'Resolution must be locked to 720p');

    // Fill in prompt and submit
    const promptInputStd = page.locator('textarea');
    await promptInputStd.fill('Veo standard extension');
    const submitBtnStd = page.getByRole('button', { name: /Generate revision/i });
    await submitBtnStd.waitFor();
    assert.equal(await submitBtnStd.isDisabled(), false, 'Submit button must be enabled for valid Veo Standard extension');
    await submitBtnStd.click();

    calls = await page.evaluate(() => (window as any).calls);
    assert.equal(calls.length, 1, 'onGenerate spy must be invoked once');
    assert.equal(calls[0].action, 'next_scene');
    assert.equal(calls[0].model, 'veo-3.1-generate-preview');
    assert.equal(calls[0].settings.durationSeconds, 8);
    assert.equal(calls[0].settings.resolution, '720p');
    assert.equal(calls[0].settings.aspectRatio, '16:9');

    // 3. Omni video: automatic duration (supportsDuration=false)
    const omniItem = {
      ...veoLiteItem,
      id: 'item-omni',
      title: 'Omni Video',
      model: 'gemini-omni-1.1-flash',
      artifact: { ...veoLiteItem.artifact, artifactId: 'art-omni' },
      videoProvenance: {
        ...veoLiteItem.videoProvenance,
        model: 'gemini-omni-1.1-flash',
        transport: 'google_interactions',
        has_interaction: true,
        extension_count_known: true,
      },
    };

    await page.evaluate((item) => (window as any).renderViewer(item, 'fine_tune'), omniItem);
    await page.getByRole('dialog', { name: /Media viewer: Omni Video/i }).waitFor();

    const omniDurSelect = page.locator('select[aria-label="Video Duration"]');
    assert.equal(await omniDurSelect.isDisabled(), true, 'Duration select must be disabled for Omni (automatic duration)');
    assert.equal(await omniDurSelect.inputValue(), '', 'Omni duration value must be empty / auto');

    // 4. Image with unavailable saved model: preserves model without fallback
    const imageWithCustomModel = {
      id: 'item-custom-img',
      title: 'Custom Image',
      filename: 'image.png',
      mediaType: 'image/png',
      kind: 'image',
      createdAt: 1000,
      formattedDate: 'Sep 20',
      formattedTime: '10:00 AM',
      dayKey: '2026-09-20',
      dayLabel: 'Today',
      sessionId: 'sess-1',
      sessionTitle: 'Session',
      workspacePath: '/ws',
      workspaceName: 'demo',
      model: 'custom-unlisted-model',
      aspectRatio: '16:9',
      resolution: '1k',
      directUrl: '/v3/sessions/sess-1/artifacts/art-img',
      artifact: {
        artifactId: 'art-img',
        sessionId: 'sess-1',
        label: 'Image',
        filename: 'image.png',
        kind: 'image',
        mediaType: 'image/png',
      },
    };

    await page.evaluate((item) => (window as any).renderViewer(item, 'fine_tune'), imageWithCustomModel);
    await page.getByRole('dialog', { name: /Media viewer: Custom Image/i }).waitFor();

    const modelSelect = page.locator('select[aria-label="Select AI model"]');
    const selectedVal = await modelSelect.inputValue();
    assert.equal(selectedVal, 'custom-unlisted-model', 'Unavailable image model must be preserved in selector, not replaced with default');

    // Submit button should be disabled because model is unavailable/not ready
    const customImgSubmit = page.getByRole('button', { name: /Generate revision/i });
    const customPromptInput = page.locator('textarea');
    await customPromptInput.fill('Change background');
    assert.equal(await customImgSubmit.isDisabled(), true, 'Submission must be disabled when model is unavailable');
  } finally {
    await browser.close();
  }
});
