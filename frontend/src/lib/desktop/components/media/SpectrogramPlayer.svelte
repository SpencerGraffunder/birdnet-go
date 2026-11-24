<!--
  SpectrogramPlayer.svelte

  Compact audio player for constrained contexts (table cells, card grids).
  Shows a spectrogram image with play/pause overlay and a scrubable timeline along
  the bottom edge. Uses the shared useAudioPlayback composable for battle-tested
  audio engine.

  For full-featured playback with clip extraction, processing controls,
  and AudioToolbar, use AudioPlayer.svelte instead.

  Props:
  - audioUrl: URL of the audio file to play
  - detectionId: Unique ID for the detection
  - spectrogramSize: Spectrogram display size - md/lg/xl (default: md)
  - denoisePreset: Playback denoise default. The player loads audioUrl for metadata and
    switches to the denoised clip on the first play (default: none)
  - onPlayStart: Callback when audio starts playing
  - onPlayEnd: Callback when audio stops playing
-->

<script lang="ts">
  import { Play, Pause, XCircle } from '@lucide/svelte';
  import { useAudioPlayback } from '$lib/utils/useAudioPlayback.svelte';
  import { useDelayedLoading } from '$lib/utils/delayedLoading.svelte';
  import { buildAppUrl } from '$lib/utils/urlHelpers';
  import { audioUrlFor } from '$lib/utils/playbackDenoise';
  import { get } from 'svelte/store';
  import { dashboardSettings } from '$lib/stores/settings';
  import { t } from '$lib/i18n';
  import { playButtonLabelKey } from '$lib/utils/audio';
  import { loggers } from '$lib/utils/logger';
  import { BIRD_NYQUIST_KHZ, MODEL_TYPE_BAT } from '$lib/utils/spectrogramAxis';
  import { useCallOverlay, callOverlayText } from '$lib/utils/useCallOverlay.svelte';
  import CallRegionOverlay from './CallRegionOverlay.svelte';
  import {
    SPECTROGRAM_REASON,
    fetchSpectrogramStatus,
    isTerminalSpectrogramReason,
    markSpectrogramSourceMissing,
  } from '$lib/utils/spectrogramStatus';
  import {
    usableDuration,
    clampSeconds,
    seekTimeFromPointer,
    seekKeyAction,
    seekSliderValueText,
  } from '$lib/utils/seek';

  const logger = loggers.audio;

  type SpectrogramSize = 'md' | 'lg' | 'xl';

  interface Props {
    audioUrl: string;
    detectionId: string;
    spectrogramSize?: SpectrogramSize;
    denoisePreset?: string;
    modelType?: string;
    onPlayStart?: () => void;
    onPlayEnd?: () => void;
  }

  let {
    audioUrl,
    detectionId,
    spectrogramSize = 'md',
    denoisePreset = '',
    modelType = '',
    onPlayStart,
    onPlayEnd,
  }: Props = $props();

  // The clip the first play switches to; '' keeps the stored clip.
  const playUrl = $derived(denoisePreset ? audioUrlFor(detectionId, denoisePreset) : '');

  // --- Audio engine (shared composable) ---
  // Initial values are intentionally captured here; audioUrl reactivity is
  // handled below via $effect calling audio.setAudioUrl(). The composable
  // reads detectionId once, for logging and the known-missing check, and callbacks are stable references.
  // svelte-ignore state_referenced_locally
  const initialAudioUrl = audioUrl;
  // svelte-ignore state_referenced_locally
  const initialPlayUrl = playUrl;
  // svelte-ignore state_referenced_locally
  const initialDetectionId = detectionId;
  // svelte-ignore state_referenced_locally
  const initialOnPlayStart = onPlayStart;
  // svelte-ignore state_referenced_locally
  const initialOnPlayEnd = onPlayEnd;
  const defaultGainDb = get(dashboardSettings)?.defaultAudioGain ?? 0;
  const audio = useAudioPlayback({
    audioUrl: initialAudioUrl,
    playUrl: initialPlayUrl,
    detectionId: initialDetectionId,
    defaultGainDb,
    onPlayStart: initialOnPlayStart,
    onPlayEnd: initialOnPlayEnd,
  });

  // React to prop changes
  $effect(() => {
    audio.setAudioUrl(audioUrl, playUrl);
  });

  // --- Spectrogram loading ---
  const spectrogramLoader = useDelayedLoading({
    delayMs: 150,
    timeoutMs: 30000,
    onTimeout: () => {
      logger.warn('Spectrogram loading timeout', { detectionId });
    },
  });

  // Spectrogram retry. When a detection's audio clip is still being produced (Extended
  // Capture defers the clip write until the capture tail is recorded, which the server
  // reports as 503 + Retry-After), the <img> onerror keeps retrying so the spectrogram
  // resolves without a manual reload. The short leading delays keep recovery snappy for
  // the common fast case (a normal encode race resolving in ~1s), while the tail caps at
  // 15s, strictly under useDelayedLoading's 30s timeoutMs, so a retry never lands after
  // the loading timeout has flipped the view to the error state and back (which would
  // flash the error icon mid-wait). The budget sums to ~2.5 min, which covers a typical
  // pending window; a maximum-length extended-capture clip can still outlast it, after
  // which the error state shows and a reload retries. The last delay repeats past the array.
  const MAX_RETRIES = 14;
  const RETRY_DELAYS = [500, 1000, 2000, 4000, 8000, 15000];
  let retryCount = $state(0);
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let cacheKey = $state(0);

  // The `<img>` element and a counter bumped on every successful load: the call overlay is
  // measured from the picture's own pixels, which only exist once it has loaded.
  let spectrogramImage = $state<HTMLImageElement | null>(null);
  let spectrogramVersion = $state(0);

  const spectrogramUrl = $derived(
    buildAppUrl(
      `/api/v2/spectrogram/${encodeURIComponent(detectionId)}?size=${spectrogramSize}&raw=true${cacheKey > 0 ? `&t=${cacheKey}` : ''}`
    )
  );

  // The status is read once, at the first failure of an image: a terminal reason
  // (the source clip is gone or cannot be rendered) ends the retries, which can
  // only repeat the same answer.
  let statusAbortController: AbortController | undefined;

  function cancelStatusCheck() {
    statusAbortController?.abort();
    statusAbortController = undefined;
  }

  function clearRetryTimer() {
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = undefined;
    }
  }

  async function endRetriesOnTerminalReason() {
    cancelStatusCheck();
    const controller = new AbortController();
    statusAbortController = controller;
    const checkedId = detectionId;
    const status = await fetchSpectrogramStatus(
      buildAppUrl(
        `/api/v2/spectrogram/${encodeURIComponent(checkedId)}/status?size=${spectrogramSize}&raw=true`
      ),
      controller.signal
    );
    if (controller.signal.aborted || !status || !isTerminalSpectrogramReason(status.reason)) return;

    logger.debug('Spectrogram cannot be rendered, ending retries', {
      detectionId: checkedId,
      reason: status.reason,
    });
    if (status.reason === SPECTROGRAM_REASON.sourceMissing) {
      markSpectrogramSourceMissing(checkedId);
    }
    clearRetryTimer();
    spectrogramLoader.setError();
  }

  // Ends a status check still in flight when the player is destroyed.
  $effect(() => cancelStatusCheck);

  // Track previous detection ID to detect changes and reset spectrogram state.
  // Uses a non-reactive variable since we only compare inside the $effect.
  // svelte-ignore state_referenced_locally
  let previousDetectionId = detectionId;
  $effect(() => {
    // Read detectionId (reactive prop) to create the dependency
    const currentId = detectionId;
    if (currentId !== previousDetectionId) {
      previousDetectionId = currentId;
      retryCount = 0;
      cacheKey = 0;
      spectrogramLoader.setLoading(true);
      cancelStatusCheck();
      clearRetryTimer();
    }
  });

  // Start loading on mount
  $effect(() => {
    if (spectrogramUrl) {
      spectrogramLoader.setLoading(true);
    }
    return clearRetryTimer;
  });

  function handleSpectrogramLoad() {
    spectrogramLoader.setLoading(false);
    retryCount = 0;
    cancelStatusCheck();
    spectrogramVersion++;
  }

  function handleSpectrogramError() {
    if (retryCount === 0) void endRetriesOnTerminalReason();
    if (retryCount < MAX_RETRIES) {
      const delay = RETRY_DELAYS[Math.min(retryCount, RETRY_DELAYS.length - 1)];
      retryCount++;
      retryTimer = setTimeout(() => {
        cacheKey = Date.now();
      }, delay);
    } else {
      spectrogramLoader.setError();
    }
  }

  // --- Timeline scrubbing ---
  // The same slider model as the full player (see $lib/utils/seek): positions are seeked
  // by time rather than by pointer geometry, so the pointer and the keyboard steps land on
  // the same places, and an unusable duration disables the control instead of lying about
  // its range. The spectrogram spans the whole clip, so x/width is the clip fraction.
  let seekBar = $state<HTMLDivElement | null>(null);
  let isScrubbing = $state(false);
  const seekMax = $derived(usableDuration(audio.duration));
  const seekNow = $derived(clampSeconds(audio.currentTime, seekMax));
  const seekValueText = $derived(seekSliderValueText(seekNow, seekMax));

  // --- Where in the clip the call is ---
  // The row asks the server for a raw spectrogram, so the image is all raster and the
  // shading needs no inset. A bird clip's axis tops out at 12 kHz; a bat clip's axis
  // follows its own capture rate, which a list row does not fetch, so a bat row shades
  // time only rather than guess at a frequency.
  const spectrogramAxesDrawn = false;
  const spectrogramNyquistKHz = $derived(modelType === MODEL_TYPE_BAT ? 0 : BIRD_NYQUIST_KHZ);
  const callOverlay = useCallOverlay({
    image: () => spectrogramImage,
    durationSec: () => seekMax,
    axesDrawn: () => spectrogramAxesDrawn,
    nyquistKHz: () => spectrogramNyquistKHz,
    version: () => spectrogramVersion,
  });
  const callOverlaySummary = $derived(callOverlayText(callOverlay.overlay, seekMax));

  function seekToClientX(clientX: number) {
    if (!seekBar || seekMax <= 0) return;
    const target = seekTimeFromPointer(clientX, seekBar.getBoundingClientRect(), seekMax);
    if (target !== null) audio.seek(target);
  }

  function handleScrubPointerDown(event: PointerEvent) {
    // Left button only for a mouse; touch and pen carry no button semantics to honour.
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    // Three separate intents sit on top of each other here: the strip scrubs, the image
    // toggles play/pause and the row opens the detail page. Stop at the strip.
    event.stopPropagation();
    if (seekMax <= 0) return;
    isScrubbing = true;
    seekBar?.setPointerCapture(event.pointerId);
    seekToClientX(event.clientX);
  }

  function handleScrubPointerMove(event: PointerEvent) {
    if (!isScrubbing) return;
    event.stopPropagation();
    seekToClientX(event.clientX);
  }

  function endScrub(event: PointerEvent) {
    if (!isScrubbing) return;
    isScrubbing = false;
    // Capture is released by the browser on pointerup/cancel; release defensively for the
    // element the pointer left mid-gesture.
    if (seekBar?.hasPointerCapture(event.pointerId)) {
      seekBar.releasePointerCapture(event.pointerId);
    }
  }

  function handleScrubKeydown(event: KeyboardEvent) {
    const action = seekKeyAction(event, seekNow, seekMax);
    if (action.preventDefault) event.preventDefault();
    if (action.target !== null) audio.seek(action.target);
  }

  // A click on the strip also bubbles to the row, which opens the detection detail.
  function handleScrubClick(event: MouseEvent) {
    event.stopPropagation();
  }

  function handlePlayClick(e: MouseEvent) {
    e.stopPropagation();
    audio.togglePlayPause();
  }
</script>

<div class="spectrogram-player" role="group" aria-label={t('media.audio.player')}>
  <!-- Spectrogram image -->
  <div class="spectrogram-image-container">
    {#if spectrogramLoader.showSpinner}
      <div class="spectrogram-overlay">
        <div
          class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-primary)] border-t-transparent"
          role="status"
          aria-label={t('components.audio.spectrogramLoadingAria')}
        ></div>
      </div>
    {/if}

    {#if spectrogramLoader.error}
      <div class="spectrogram-overlay">
        <XCircle class="size-5 text-[var(--color-base-content)]/30" aria-hidden="true" />
      </div>
    {:else}
      <img
        bind:this={spectrogramImage}
        src={spectrogramUrl}
        alt={t('components.audio.spectrogramAlt')}
        title={callOverlaySummary || undefined}
        decoding="async"
        class="spectrogram-img"
        class:invisible={spectrogramLoader.loading}
        onload={handleSpectrogramLoad}
        onerror={handleSpectrogramError}
      />
    {/if}

    <!-- Where the bird is in this clip: shaded columns over the calls, so a row can be
         read before it is played. The same words are available to screen readers. -->
    {#if callOverlay.overlay}
      <CallRegionOverlay
        overlay={callOverlay.overlay}
        durationSec={seekMax}
        nyquistKHz={spectrogramNyquistKHz}
        axesDrawn={spectrogramAxesDrawn}
      />
      <span class="sr-only">{callOverlaySummary}</span>
    {/if}

    <!-- Play/pause overlay button -->
    <button
      class="play-overlay"
      class:is-playing={audio.isPlaying}
      onclick={handlePlayClick}
      disabled={audio.isLoading}
      aria-label={t(playButtonLabelKey(audio.isLoading, audio.isPlaying))}
    >
      {#if audio.isLoading}
        <div
          class="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin"
        ></div>
      {:else if audio.isPlaying}
        <Pause class="size-4" />
      {:else}
        <Play class="size-4" />
      {/if}
    </button>

    <!-- Timeline along the bottom edge: click or drag to scrub, arrows to step. It is
         always present for a clip (a control you must start playing before you can use is
         the clunky part), and reports aria-disabled while the length is still unknown. -->
    <div
      bind:this={seekBar}
      class="seek-strip"
      class:is-scrubbing={isScrubbing}
      role="slider"
      tabindex="0"
      aria-label={t('media.audio.seek')}
      aria-valuemin={0}
      aria-valuemax={seekMax}
      aria-valuenow={seekNow}
      aria-valuetext={seekValueText}
      aria-disabled={seekMax <= 0 ? 'true' : undefined}
      onpointerdown={handleScrubPointerDown}
      onpointermove={handleScrubPointerMove}
      onpointerup={endScrub}
      onpointercancel={endScrub}
      onkeydown={handleScrubKeydown}
      onclick={handleScrubClick}
    >
      <div class="seek-rail"></div>
      <div class="seek-fill" style:width="{audio.progress}%"></div>
      <div class="seek-thumb" style:left="{audio.progress}%"></div>
    </div>
  </div>

  <!-- Audio error -->
  {#if audio.error}
    <div class="audio-error" role="alert" aria-live="assertive">
      <span class="text-xs text-[var(--text-error)]">{audio.error}</span>
    </div>
  {/if}
</div>

<style>
  .spectrogram-player {
    width: 100%;
    max-width: 200px;
  }

  .spectrogram-image-container {
    position: relative;
    width: 100%;
    background: linear-gradient(to bottom, rgb(128 128 128 / 0.1), rgb(128 128 128 / 0.05));
    border-radius: 0.375rem;
    overflow: hidden;
  }

  .spectrogram-img {
    display: block;
    width: 100%;
    height: auto;
    border-radius: 0.375rem;
  }

  .spectrogram-overlay {
    display: flex;
    align-items: center;
    justify-content: center;
    aspect-ratio: 2 / 1;
    width: 100%;
  }

  /* Play button: always visible at reduced opacity, full opacity on hover */
  .play-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: white;
    background: rgb(0 0 0 / 0.25);
    opacity: 0.7;
    transition: opacity 0.15s ease;
    cursor: pointer;
    border: none;
    padding: 0;
  }

  .play-overlay:hover {
    opacity: 1;
    background: rgb(0 0 0 / 0.35);
  }

  .play-overlay.is-playing {
    opacity: 0;
  }

  .play-overlay.is-playing:hover {
    opacity: 1;
  }

  /* Timeline: the same 3px line as before, inside a 14px invisible strip so the control
     is grabbable by touch and a thumb can appear on hover/focus without changing the
     resting look of the row. */
  .seek-strip {
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    height: 14px;
    z-index: 2;
    cursor: pointer;
    touch-action: none;
    user-select: none;
  }

  .seek-rail,
  .seek-fill {
    position: absolute;
    bottom: 0;
    left: 0;
    height: 3px;
  }

  .seek-rail {
    right: 0;
    background: rgb(255 255 255 / 0.2);
  }

  .seek-fill {
    width: 0;
    background: var(--color-primary);
    transition:
      width 0.1s linear,
      height 0.15s ease;
  }

  /* Hidden by scale rather than display, so the appearance transition runs.
     The dot sits on the rail's centre line, not above it: the rail is 3px tall and flush
     with the strip's bottom edge (centre 1.5px up), so a 9px dot starts at 1.5px - 4.5px. */
  .seek-thumb {
    position: absolute;
    bottom: calc(1.5px - 4.5px);
    width: 9px;
    height: 9px;
    border: 1px solid var(--color-base-100);
    border-radius: 50%;
    background: var(--color-primary);
    transform: translate(-50%, 0) scale(0);
    transition: transform 0.15s ease;
  }

  .seek-strip:hover .seek-fill,
  .seek-strip:focus-visible .seek-fill,
  .seek-strip.is-scrubbing .seek-fill {
    height: 4px;
  }

  .seek-strip:hover .seek-thumb,
  .seek-strip:focus-visible .seek-thumb,
  .seek-strip.is-scrubbing .seek-thumb {
    transform: translate(-50%, 0) scale(1);
  }

  .seek-strip:focus-visible {
    outline: 2px solid var(--color-primary);
    outline-offset: 1px;
  }

  @media (prefers-reduced-motion: reduce) {
    .seek-fill,
    .seek-thumb {
      transition: none;
    }
  }

  .audio-error {
    padding: 0.125rem 0.25rem;
    text-align: center;
  }
</style>
