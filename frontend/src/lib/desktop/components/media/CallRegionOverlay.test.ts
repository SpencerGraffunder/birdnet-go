/**
 * CallRegionOverlay.test.ts
 *
 * The shading that tells a listener where in a recording the bird is. The measurement is
 * covered by callOverlay.test.ts (including a clip captured from the live server); what is
 * tested here is the part only this component has to get right: turning a measurement into
 * positions on the picture, and knowing when to draw nothing.
 */
import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import CallRegionOverlay from './CallRegionOverlay.svelte';
import { callOverlayText } from '$lib/utils/useCallOverlay.svelte';
import type { CallOverlay } from '$lib/utils/callOverlay';

// A spectrogram as the server draws it with its axes painted in: 1170x591 with the plot
// between x=58..1084 and y=30..543, over a 61-second clip. The percentages below come
// from that geometry by arithmetic, not by running the component's own code.
const PAINTED: CallOverlay = {
  imageSize: { width: 1170, height: 591 },
  rect: { x: 58, y: 30, width: 1026, height: 513 },
  regions: [
    { startSec: 5, endSec: 7, prominence: 0.09, primary: true },
    { startSec: 40, endSec: 41, prominence: 0.04, primary: false },
  ],
  band: { lowKHz: 1, highKHz: 2.5 },
};

const RAW: CallOverlay = {
  imageSize: { width: 514, height: 257 },
  rect: { x: 0, y: 0, width: 514, height: 257 },
  regions: [{ startSec: 3, endSec: 5, prominence: 0.1, primary: true }],
  band: { lowKHz: 2, highKHz: 4 },
};

interface OverlayProps {
  overlay: CallOverlay | null;
  durationSec: number;
  nyquistKHz: number;
  axesDrawn: boolean;
}

function mount(overrides: Partial<OverlayProps> = {}) {
  return render(CallRegionOverlay, {
    props: {
      overlay: PAINTED,
      durationSec: 61,
      nyquistKHz: 12,
      axesDrawn: true,
      ...overrides,
    },
  });
}

function styleOf(el: Element | null, property: string): number {
  return parseFloat((el as HTMLElement | null)?.style.getPropertyValue(property) ?? '');
}

describe('CallRegionOverlay', () => {
  it('shades the call at the time it happens', () => {
    const { container } = mount();
    const spans = container.querySelectorAll<HTMLElement>('.call-span');

    expect(spans).toHaveLength(2);
    // x = 58 + 1026 * (5/61), out of a 1170 px image.
    expect(styleOf(spans[0], 'left')).toBeCloseTo(12.1452, 3);
    expect(styleOf(spans[0], 'width')).toBeCloseTo(2.8752, 3);
  });

  it('highlights the strongest call more strongly', () => {
    const { container } = mount();
    const spans = container.querySelectorAll('.call-span');

    expect(spans[0].classList.contains('is-primary')).toBe(true);
    expect(spans[1].classList.contains('is-primary')).toBe(false);
  });

  it('covers the plot the axes leave, not the whole picture', () => {
    const { container } = mount();
    const span = container.querySelector<HTMLElement>('.call-span');

    // 48 px of axis below the plot and 513 px of plot, out of 591 px.
    expect(styleOf(span, 'bottom')).toBeCloseTo(8.1218, 3);
    expect(styleOf(span, 'height')).toBeCloseTo(86.802, 3);
  });

  it('spans the whole image when the axes are not painted into it', () => {
    const { container } = mount({ overlay: RAW, durationSec: 10, axesDrawn: false });
    const span = container.querySelector<HTMLElement>('.call-span');

    expect(styleOf(span, 'bottom')).toBeCloseTo(0, 3);
    expect(styleOf(span, 'height')).toBeCloseTo(100, 3);
    // x runs edge to edge: 3/10 and 2/10 of the width.
    expect(styleOf(span, 'left')).toBeCloseTo(30, 3);
    expect(styleOf(span, 'width')).toBeCloseTo(20, 3);
  });

  it('places the frequency band on the rows the axis names', () => {
    const { container } = mount();
    const band = container.querySelector<HTMLElement>('.call-band');

    // 1 kHz sits 42.75 px above the raster floor; the band is 1.5 kHz tall.
    expect(styleOf(band, 'bottom')).toBeCloseTo(15.3553, 3);
    expect(styleOf(band, 'height')).toBeCloseTo(10.8503, 3);
  });

  it('marks time but no frequency when the axis is unknown', () => {
    // A bat clip whose capture rate the player has not resolved: guessing a frequency
    // here would point at the wrong part of the picture.
    const { container } = mount({ nyquistKHz: 0 });

    expect(container.querySelectorAll('.call-span')).toHaveLength(2);
    expect(container.querySelector('.call-band')).toBeNull();
  });

  it('draws nothing without a measurement, and claims nothing to a screen reader', () => {
    const { container } = mount({ overlay: null });
    expect(container.querySelector('.call-overlay')).toBeNull();

    const { container: painted } = mount();
    expect(painted.querySelector('.call-overlay')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('refuses to place a call on a clip of unknown length', () => {
    const { container } = mount({ durationSec: 0 });
    expect(container.querySelector('.call-span')).toBeNull();
  });
});

describe('callOverlayText', () => {
  it('says where the loudest call is and what band it covers', () => {
    expect(callOverlayText(PAINTED, 61)).toBe(
      'Loudest call at 0:05–0:07, 1.0–2.5 kHz. Other calls in this clip are also shaded.'
    );
  });

  it('leaves out the promise of more calls when there are none', () => {
    const onlyOne: CallOverlay = { ...PAINTED, regions: [PAINTED.regions[0]] };
    expect(callOverlayText(onlyOne, 61)).toBe('Loudest call at 0:05–0:07, 1.0–2.5 kHz');
  });

  it('says time alone when the frequency would be a guess', () => {
    const noBand: CallOverlay = { ...PAINTED, regions: [PAINTED.regions[0]], band: null };
    expect(callOverlayText(noBand, 61)).toBe('Loudest call at 0:05–0:07');
  });

  it('has nothing to say without a measurement', () => {
    expect(callOverlayText(null, 61)).toBe('');
    expect(callOverlayText(PAINTED, 0)).toBe('');
  });
});
