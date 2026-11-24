/**
 * SpectrogramPlayer.callOverlay.test.ts
 *
 * The detections list row, shaded where in the clip the bird is. jsdom provides no canvas,
 * so the pixels come back from a stub: what is under test here is the wiring — that the row
 * measures the picture it is showing, waits for a clip length worth placing time on, and
 * renders what comes back. The detector itself has its own tests, including against
 * spectrogram bytes captured from the live server (callOverlay.fixture.ts).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { writable } from 'svelte/store';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import SpectrogramPlayer from './SpectrogramPlayer.svelte';

// Mock state the tests mutate before mounting, in the shape the component reads.
const mockEngine = { duration: 30 };
const mockDashboardSettings = writable<{ defaultAudioGain?: number }>({ defaultAudioGain: 0 });

vi.mock('$lib/stores/settings', () => ({
  dashboardSettings: {
    subscribe: (fn: (val: { defaultAudioGain?: number }) => void) =>
      mockDashboardSettings.subscribe(fn),
  },
}));

vi.mock('$lib/utils/useAudioPlayback.svelte', () => ({
  useAudioPlayback: () => ({
    get duration() {
      return mockEngine.duration;
    },
    currentTime: 0,
    progress: 0,
    isPlaying: false,
    isLoading: false,
    error: null,
    togglePlayPause: vi.fn(),
    seek: vi.fn(),
    setAudioUrl: vi.fn(),
  }),
}));

/** A raw spectrogram: dark everywhere except one bright block, mid-clip and mid-band. */
function brightBlock(width: number, height: number) {
  const data = new Uint8ClampedArray(width * height * 4).fill(8);
  for (let y = 20; y <= 39; y++) {
    for (let x = 40; x <= 59; x++) {
      const p = (y * width + x) * 4;
      data[p] = 240;
      data[p + 1] = 240;
      data[p + 2] = 240;
      data[p + 3] = 255;
    }
  }
  return { width, height, data };
}

const IMAGE_WIDTH = 100;
const IMAGE_HEIGHT = 50;

/**
 * Answers the canvas read the composable performs. `readable: false` stands for a canvas
 * the browser refuses to hand back, which must leave the row unannotated rather than
 * half-annotated.
 */
function stubCanvas(readable = true) {
  const pixels = brightBlock(IMAGE_WIDTH, IMAGE_HEIGHT);
  return vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() =>
    readable
      ? ({
          drawImage: vi.fn(),
          getImageData: vi.fn(() => ({ ...pixels, colorSpace: 'srgb' })),
        } as unknown as CanvasRenderingContext2D)
      : null
  );
}

async function mountPlayer(props: Record<string, unknown> = {}) {
  const view = render(SpectrogramPlayer, {
    props: { audioUrl: '/api/v2/audio/1', detectionId: '1', ...props },
  });
  const img = view.container.querySelector('img');
  if (!img) throw new Error('the row renders no spectrogram image');
  Object.defineProperty(img, 'naturalWidth', { value: IMAGE_WIDTH, configurable: true });
  Object.defineProperty(img, 'naturalHeight', { value: IMAGE_HEIGHT, configurable: true });
  Object.defineProperty(img, 'complete', { value: true, configurable: true });
  await fireEvent.load(img);
  return { ...view, img };
}

describe('SpectrogramPlayer call overlay', () => {
  beforeEach(() => {
    mockEngine.duration = 30;
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('shades the row where the call is', async () => {
    stubCanvas();
    const { container } = await mountPlayer();

    const spans = container.querySelectorAll<HTMLElement>('.call-span');
    expect(spans).toHaveLength(1);
    // The bright block covers columns 40..59 of a raw image whose raster is the whole
    // picture, so the shading belongs on the middle fifth of the row.
    expect(parseFloat(spans[0].style.left)).toBeCloseTo(40, 0);
    expect(parseFloat(spans[0].style.width)).toBeCloseTo(20, 0);
  });

  it('marks the frequencies the call occupies', async () => {
    stubCanvas();
    const { container } = await mountPlayer();

    const band = container.querySelector<HTMLElement>('.call-band');
    if (!band) throw new Error('a bird row with painted rows should mark the band');
    // Bright rows 20..39 of 50 sit around 41% up from the bottom of the picture; the
    // detector widens a band slightly, so the assertion is on its centre, not its edges.
    const bottom = parseFloat(band.style.bottom);
    const height = parseFloat(band.style.height);
    expect(bottom + height / 2).toBeCloseTo(41, -1);
    expect(height).toBeGreaterThan(10);
  });

  it('marks time but no frequency on a bat row', async () => {
    // A bat axis depends on the clip's capture rate, which a list row does not fetch.
    stubCanvas();
    const { container } = await mountPlayer({ modelType: 'bat' });

    expect(container.querySelectorAll('.call-span')).toHaveLength(1);
    expect(container.querySelector('.call-band')).toBeNull();
  });

  it('leaves the row alone when the picture cannot be read', async () => {
    stubCanvas(false);
    const { container, img } = await mountPlayer();

    expect(container.querySelector('.call-span')).toBeNull();
    expect(img.getAttribute('title')).toBeNull();
  });

  it('waits for a clip length worth placing time on', async () => {
    mockEngine.duration = 0;
    stubCanvas();
    const { container } = await mountPlayer();

    expect(container.querySelector('.call-span')).toBeNull();
  });

  it('says in words where the call is, for the tooltip and for screen readers', async () => {
    stubCanvas();
    const { container, img } = await mountPlayer();

    const spoken = container.querySelector('.sr-only')?.textContent ?? '';
    expect(spoken).toContain('Loudest call at');
    expect(img.getAttribute('title')).toBe(spoken);
  });
});
