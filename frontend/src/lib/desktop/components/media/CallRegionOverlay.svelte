<!--
  CallRegionOverlay.svelte

  Highlights where in a recording the detected call is, on top of the spectrogram the
  player is already showing: a shaded column for each moment the bird is singing, and a
  shaded strip for the frequencies that moment occupies.

  Why this exists: a detection record carries the capture window, not the call, so
  recordings routinely hold seconds of wind on either side of the bird — and sometimes a
  loud unrelated sound in the foreground. The shading is what tells you where to listen
  before you press play. The measurement is in $lib/utils/callOverlay; this component only
  positions it, using the same mapping the frequency axis uses so both land on the same
  rows of pixels.

  It is deliberately inert: no pointer events, no text over the picture. The numbers go in
  the player's tooltip and in the screen-reader text (see callOverlayText), so nothing
  covers the spectrogram the user is looking at.

  Props:
  - overlay: the measured overlay (null draws nothing)
  - durationSec: clip length, which places time on the raster
  - nyquistKHz: top of the frequency axis (12 kHz for birds, half the bat capture rate)
  - axesDrawn: whether the served image carries the server-painted axes
-->

<script lang="ts">
  import {
    frequencyBottomPercent,
    timeLeftPercent,
    type CallOverlay,
    type SpectrogramImageGeometry,
  } from '$lib/utils/callOverlay';

  interface Props {
    overlay: CallOverlay | null;
    durationSec: number;
    nyquistKHz: number;
    axesDrawn: boolean;
  }

  let { overlay, durationSec, nyquistKHz, axesDrawn }: Props = $props();

  const geometry = $derived<SpectrogramImageGeometry | null>(
    overlay
      ? {
          width: overlay.imageSize.width,
          height: overlay.imageSize.height,
          axesDrawn,
        }
      : null
  );

  /** The plot area, as percentages of the image box: highlights never cover the axes. */
  const plot = $derived.by(() => {
    if (!overlay || !geometry) return null;
    const { rect } = overlay;
    const { width, height } = overlay.imageSize;
    if (!(width > 0) || !(height > 0)) return null;
    return {
      bottomPercent: ((height - rect.y - rect.height) / height) * 100,
      heightPercent: (rect.height / height) * 100,
    };
  });

  /** One column per call, placed by time. */
  const spans = $derived.by(() => {
    if (!overlay || !geometry || !plot) return [];
    return overlay.regions.flatMap(region => {
      const left = timeLeftPercent(region.startSec, durationSec, geometry);
      const right = timeLeftPercent(region.endSec, durationSec, geometry);
      if (left === null || right === null || right <= left) return [];
      return [
        {
          leftPercent: left,
          widthPercent: right - left,
          primary: region.primary,
          key: `${region.startSec.toFixed(3)}-${region.endSec.toFixed(3)}`,
        },
      ];
    });
  });

  /** The frequency band of the strongest call, placed on the axis. */
  const band = $derived.by(() => {
    if (!overlay?.band || !geometry) return null;
    const bottom = frequencyBottomPercent(overlay.band.lowKHz, nyquistKHz, geometry);
    const top = frequencyBottomPercent(overlay.band.highKHz, nyquistKHz, geometry);
    if (bottom === null || top === null || top <= bottom) return null;
    return { bottomPercent: bottom, heightPercent: top - bottom };
  });
</script>

{#if plot}
  <div class="call-overlay" aria-hidden="true">
    {#if band}
      <div
        class="call-band"
        style:bottom="{band.bottomPercent}%"
        style:height="{band.heightPercent}%"
      ></div>
    {/if}
    {#each spans as span (span.key)}
      <div
        class="call-span"
        class:is-primary={span.primary}
        style:left="{span.leftPercent}%"
        style:width="{span.widthPercent}%"
        style:bottom="{plot.bottomPercent}%"
        style:height="{plot.heightPercent}%"
      ></div>
    {/each}
  </div>
{/if}

<style>
  .call-overlay {
    position: absolute;
    inset: 0;
    pointer-events: none;

    /* No z-index on purpose: the highlight sits above the picture by document order, and
       whatever the player paints later — its controls, playhead, a clip selection — stays
       on top of it. The shading is decoration, never something to cover what the user is
       doing. */
  }

  /* Time: a column over the raster, strongest call a little brighter. The tints are
     color-mixed from the theme's primary, as elsewhere in the UI, so they follow the
     active theme instead of assuming a fixed hue. */
  .call-span {
    position: absolute;
    min-width: 2px;
    background: color-mix(in srgb, var(--color-primary) 16%, transparent);
    border-left: 1px solid color-mix(in srgb, var(--color-primary) 55%, transparent);
    border-right: 1px solid color-mix(in srgb, var(--color-primary) 55%, transparent);
  }

  .call-span.is-primary {
    background: color-mix(in srgb, var(--color-primary) 26%, transparent);
  }

  /* Frequency: the strip the call lives in, across the whole clip so it can be read
     against the axis without claiming the call lasts that long. */
  .call-band {
    position: absolute;
    left: 0;
    right: 0;
    background: color-mix(in srgb, var(--color-primary) 8%, transparent);
    border-top: 1px solid color-mix(in srgb, var(--color-primary) 30%, transparent);
    border-bottom: 1px solid color-mix(in srgb, var(--color-primary) 30%, transparent);
  }
</style>
