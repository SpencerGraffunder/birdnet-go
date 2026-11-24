/**
 * useCallOverlay.svelte.ts
 *
 * Reads the spectrogram a player has already drawn and reports where in the clip the call
 * is, so the player can highlight it. The maths lives in `callOverlay.ts` (pure, tested);
 * this file owns the one browser job: getting the image's pixels.
 *
 * Why the pixels and not the API: a stored detection has no position for the call —
 * see the header of `callOverlay.ts` — while the image on screen is a picture of exactly
 * this clip. Reusing it means no second request, no new endpoint, and clips recorded
 * before this feature existed get the highlight too.
 *
 * Reading is cheap and happens once per image: the effect re-runs when the image is
 * (re)loaded (`version`), when the clip length first becomes known, and when the axis or
 * the drawn-chrome setting changes. Anything unreadable — a detached element, a canvas the
 * browser refuses to hand back — leaves the overlay null, and the player simply shows the
 * spectrogram as it always did.
 */
import { callOverlayFromPixels, type CallOverlay } from './callOverlay';
import { BIRD_NYQUIST_KHZ } from './spectrogramAxis';
import { formatTime } from './formatters';
import { loggers } from './logger';
import { t } from '$lib/i18n';

const logger = loggers.audio;

/** What the caller knows about the spectrogram it is showing. */
export interface CallOverlaySource {
  /** The `<img>` element, or null before it exists. */
  image: () => HTMLImageElement | null;
  /** Clip length in seconds; 0 until the audio element reports a usable duration. */
  durationSec: () => number;
  /** Whether the served image carries the server-painted axes. */
  axesDrawn: () => boolean;
  /** kHz at the top of the frequency axis; birds are 12, bats depend on the clip rate. */
  nyquistKHz?: () => number;
  /** Bump to re-measure the current image — call it from the image's `onload`. */
  version: () => number;
}

/** One canvas shared by every player: measuring never allocates a second one. */
let scratchCanvas: HTMLCanvasElement | null = null;

function readPixels(
  image: HTMLImageElement
): { pixels: ImageData; width: number; height: number } | null {
  const width = image.naturalWidth;
  const height = image.naturalHeight;
  if (!(width > 0) || !(height > 0)) return null;

  scratchCanvas ??= document.createElement('canvas');
  scratchCanvas.width = width;
  scratchCanvas.height = height;
  const context = scratchCanvas.getContext('2d', { willReadFrequently: true });
  if (!context) return null;
  context.drawImage(image, 0, 0, width, height);
  try {
    return { pixels: context.getImageData(0, 0, width, height), width, height };
  } catch (error) {
    // A canvas that was drawn from a cross-origin image cannot be read back. The
    // spectrogram is served same-origin, so this is a proxy or CDN change, not a
    // normal state: report it once per image and stay unannotated.
    logger.debug('Spectrogram pixels could not be read', {
      width,
      height,
      message: error instanceof Error ? error.message : String(error),
    });
    return null;
  }
}

/** The measured overlay, or null when there is nothing trustworthy to draw. */
export interface CallOverlayState {
  readonly overlay: CallOverlay | null;
}

export function useCallOverlay(source: CallOverlaySource): CallOverlayState {
  let overlay = $state<CallOverlay | null>(null);

  $effect(() => {
    // Dependencies first, all read before anything is computed: the image element, its
    // load counter, the clip length, the axis, and whether the axes are painted in.
    const version = source.version();
    const image = source.image();
    const durationSec = source.durationSec();
    const axesDrawn = source.axesDrawn();
    const nyquistKHz = source.nyquistKHz?.() ?? BIRD_NYQUIST_KHZ;

    // Measure into a local, and read only that below. Reading `overlay` here — the state
    // this effect writes — would make the effect depend on its own output, and since every
    // measurement is a new object it would re-run until the update depth ran out. In a
    // browser that is not an error message, it is a tab that never goes idle.
    let measured: CallOverlay | null = null;
    if (image?.complete && durationSec > 0) {
      const read = readPixels(image);
      if (read) {
        measured = callOverlayFromPixels({
          pixels: read.pixels,
          width: read.width,
          height: read.height,
          axesDrawn,
          durationSec,
          nyquistKHz,
        });
      }
    }

    overlay = measured;
    logger.debug('Call overlay measured', {
      version,
      regions: measured?.regions.length ?? 0,
      band: measured?.band
        ? `${measured.band.lowKHz.toFixed(1)}-${measured.band.highKHz.toFixed(1)} kHz`
        : null,
    });
  });

  return {
    get overlay() {
      return overlay;
    },
  };
}

/**
 * The call in words, for a player's tooltip and screen-reader text: where the loudest call
 * sits in the clip and which part of the frequency axis it covers. Empty when nothing was
 * measured, so a caller can hand it straight to `title` and render nothing.
 *
 * The picture says where to listen; this says what the shading means in numbers, which is
 * the part a shaded rectangle cannot tell you.
 */
export function callOverlayText(overlay: CallOverlay | null, durationSec: number): string {
  if (!overlay || !(durationSec > 0)) return '';
  // The detector marks one region primary whenever it found any, so this lookup is the
  // whole selection rather than a preference with a fallback.
  const primary = overlay.regions.find(region => region.primary);
  if (!primary) return '';

  const time = t('media.spectrogram.callTimeRange', {
    start: formatTime(primary.startSec),
    end: formatTime(primary.endSec),
  });
  const text = overlay.band
    ? t('media.spectrogram.callAtBand', {
        time,
        band: t('media.spectrogram.callBandRange', {
          low: overlay.band.lowKHz.toFixed(1),
          high: overlay.band.highKHz.toFixed(1),
        }),
      })
    : t('media.spectrogram.callAt', { time });

  // Only when there really are others: saying it for a single-call clip would be a lie.
  // The two sentences are joined here because the first one carries no full stop — the
  // tooltip is a sentence, not a concatenation of parts.
  return overlay.regions.length > 1 ? `${text}. ${t('media.spectrogram.callOtherShaded')}` : text;
}
