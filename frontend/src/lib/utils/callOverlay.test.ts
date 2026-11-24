import { describe, it, expect } from 'vitest';
import { BIRD_NYQUIST_KHZ } from './spectrogramAxis';
import { winterWrenClip } from './callOverlay.fixture';
import {
  BAND_MIN_KHZ,
  CALL_REGION_MAD_MULTIPLIER,
  CALL_REGION_MAX_REGIONS,
  SPECTROGRAM_IMAGE_MARGINS,
  bandFromRows,
  callOverlayFromPixels,
  columnsForSeconds,
  columnEnergy,
  detectCallRegions,
  frequencyBottomPercent,
  greyRasterFromPixels,
  median,
  medianAbsoluteDeviation,
  overlayBox,
  rasterRect,
  rowEnergy,
  rowMedianEnergy,
  timeLeftPercent,
  type GreyRaster,
  type PixelSource,
} from './callOverlay';

// Geometry expectations are written as literals: a changed margin constant has to
// change them on purpose. The 1170x591 case is a served clip measured on UnCube —
// the painted grid column sits at x = 57 and the 25 painted frequency ticks span
// y[30,542], which is what these numbers describe.
describe('rasterRect', () => {
  it('finds the plot area of a full-size image with painted axes', () => {
    expect(rasterRect(1170, 591, true)).toEqual({ x: 58, y: 30, width: 1026, height: 513 });
  });

  it('finds the plot area of the thumbnail size the detection list asks for', () => {
    expect(rasterRect(514, 257, true)).toEqual({ x: 58, y: 30, width: 370, height: 179 });
  });

  it('treats a raw image as nothing but plot area', () => {
    expect(rasterRect(1026, 513, false)).toEqual({ x: 0, y: 0, width: 1026, height: 513 });
  });

  it('gives up on an image smaller than the margins rather than inventing a plot', () => {
    expect(rasterRect(144, 78, true)).toBeNull();
    expect(rasterRect(143, 500, true)).toBeNull();
    expect(rasterRect(500, 77, true)).toBeNull();
    expect(rasterRect(0, 0, false)).toBeNull();
  });

  it('gives up on sizes that are not numbers', () => {
    expect(rasterRect(Number.NaN, 591, true)).toBeNull();
    expect(rasterRect(1170, Number.POSITIVE_INFINITY, true)).toBeNull();
  });

  it('mirrors the renderer margins', () => {
    expect(SPECTROGRAM_IMAGE_MARGINS).toEqual({ left: 58, above: 30, below: 48, right: 86 });
  });
});

function rgba(width: number, height: number, fill: (x: number, y: number) => number): PixelSource {
  const data = new Uint8ClampedArray(width * height * 4);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const v = fill(x, y);
      const p = (y * width + x) * 4;
      data[p] = v;
      data[p + 1] = Math.floor(v / 2);
      data[p + 2] = 0;
      data[p + 3] = 255;
    }
  }
  return { width, height, data };
}

describe('greyRasterFromPixels', () => {
  it('keeps the brightest channel so palette brightness survives', () => {
    const raster = greyRasterFromPixels(rgba(2, 1, x => (x === 0 ? 40 : 200)));
    const values = Array.from(raster?.values ?? []);
    expect(values).toHaveLength(2);
    expect(values[0]).toBeCloseTo(40 / 255, 5);
    expect(values[1]).toBeCloseTo(200 / 255, 5);
  });

  it('returns null for input that cannot describe an image', () => {
    expect(greyRasterFromPixels({ width: 0, height: 4, data: [] })).toBeNull();
    expect(
      greyRasterFromPixels({ width: 2, height: 2, data: new Uint8ClampedArray(12) })
    ).toBeNull();
  });
});

// A raster of `width` columns and `height` rows whose brightness is `column(x) + row(y)`,
// which keeps the tests below readable.
function synthRaster(
  width: number,
  height: number,
  column: (x: number) => number,
  row = (_y: number) => 0
): GreyRaster {
  const values = new Float32Array(width * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) values[y * width + x] = column(x) + row(y);
  }
  return { width, height, values };
}

describe('columnEnergy', () => {
  it('averages each column inside the rectangle', () => {
    const raster = synthRaster(4, 2, x => x);
    expect(Array.from(columnEnergy(raster, { x: 0, y: 0, width: 4, height: 2 }))).toEqual([
      0, 1, 2, 3,
    ]);
  });

  it('ignores columns outside the rectangle', () => {
    const raster = synthRaster(6, 1, x => x);
    expect(Array.from(columnEnergy(raster, { x: 2, y: 0, width: 3, height: 1 }))).toEqual([
      2, 3, 4,
    ]);
  });
});

describe('rowEnergy', () => {
  it('averages each row over the requested columns only', () => {
    const raster = synthRaster(
      6,
      3,
      x => x,
      y => y * 10
    );
    expect(Array.from(rowEnergy(raster, { x: 0, y: 0, width: 6, height: 3 }, 4, 6))).toEqual([
      4.5, 14.5, 24.5,
    ]);
  });

  it('never reads outside the rectangle or past its right edge', () => {
    const raster = synthRaster(
      4,
      2,
      x => x,
      y => y
    );
    expect(Array.from(rowEnergy(raster, { x: 0, y: 0, width: 4, height: 2 }, -5, 99))).toEqual([
      1.5, 2.5,
    ]);
  });
});

describe('median and medianAbsoluteDeviation', () => {
  it('takes the middle of an odd and even set', () => {
    expect(median([3, 1, 2])).toBe(2);
    expect(median([4, 1, 3, 2])).toBe(2.5);
    expect(median([])).toBe(0);
  });

  it('measures spread around a supplied centre', () => {
    expect(medianAbsoluteDeviation([1, 1, 1, 5, 5], 1)).toBe(0);
    expect(medianAbsoluteDeviation([2, 4])).toBe(1);
  });
});

// 100 columns for a 10 s clip, so one column is 0.1 s.
const COLUMNS = 100;
const TEN_SECONDS = 10;

/** Room tone everywhere, with calls (height `level`) at the given column ranges. */
function clipWithBursts(bursts: [number, number][], level = 0.8, floor = 0.02): Float32Array {
  const values = new Float32Array(COLUMNS).fill(floor);
  for (const [from, to] of bursts) {
    for (let i = from; i < to; i++) values[i] = level;
  }
  return values;
}

describe('detectCallRegions', () => {
  it('finds two separated calls where they are in the clip', () => {
    const regions = detectCallRegions(
      clipWithBursts([
        [10, 20],
        [70, 80],
      ]),
      { durationSec: TEN_SECONDS }
    );
    expect(regions).toHaveLength(2);
    expect(regions[0]?.startSec).toBeCloseTo(1, 5);
    expect(regions[0]?.endSec).toBeCloseTo(2, 5);
    expect(regions[1]?.startSec).toBeCloseTo(7, 5);
    expect(regions[1]?.endSec).toBeCloseTo(8, 5);
  });

  it('calls the strongest burst the primary one', () => {
    const values = clipWithBursts(
      [
        [10, 20],
        [70, 80],
      ],
      0.4
    );
    for (let i = 70; i < 80; i++) values[i] = 0.9;
    const regions = detectCallRegions(values, { durationSec: TEN_SECONDS });
    expect(regions.map(r => r.primary)).toEqual([false, true]);
  });

  it('joins bursts closer than the merge gap, since they are one call', () => {
    // One column is 0.1 s here, and the merge gap is 0.15 s, so a one-column
    // quiet stretch sits inside a single call.
    const mid = 40;
    const values = clipWithBursts([
      [mid, mid + 5],
      [mid + 6, mid + 11],
    ]);
    const regions = detectCallRegions(values, { durationSec: TEN_SECONDS });
    expect(regions).toHaveLength(1);
  });

  it('splits bursts further apart than the merge gap', () => {
    // A five-column (0.5 s) quiet stretch is longer than one call's pause.
    const mid = 30;
    const values = clipWithBursts([
      [mid, mid + 5],
      [mid + 10, mid + 15],
    ]);
    expect(detectCallRegions(values, { durationSec: TEN_SECONDS })).toHaveLength(2);
  });

  it('drops a single-column click as too short to be a call', () => {
    const values = clipWithBursts([[50, 51]]); // 0.1 s
    expect(detectCallRegions(values, { durationSec: TEN_SECONDS })).toEqual([]);
  });

  it('reports nothing for a clip with no spread at all', () => {
    const silence = new Float32Array(COLUMNS);
    expect(detectCallRegions(silence, { durationSec: TEN_SECONDS })).toEqual([]);
  });

  it('reports nothing when there is no clip to place regions on', () => {
    const values = clipWithBursts([[10, 20]]);
    expect(detectCallRegions(values, { durationSec: 0 })).toEqual([]);
    expect(detectCallRegions(values, { durationSec: Number.NaN })).toEqual([]);
    expect(detectCallRegions(new Float32Array(1), { durationSec: TEN_SECONDS })).toEqual([]);
  });

  it('keeps the strongest few bursts when a clip is full of them', () => {
    const bursts: [number, number][] = [];
    for (let start = 0; start < COLUMNS; start += 10) bursts.push([start, start + 4]);
    const values = clipWithBursts(bursts);
    // Weaken the first two calls without touching the quiet between them, so only
    // their prominence drops.
    for (const [from, to] of bursts.slice(0, 2)) {
      for (let i = from; i < to; i++) values[i] = 0.25;
    }
    const regions = detectCallRegions(values, { durationSec: TEN_SECONDS });
    expect(regions).toHaveLength(CALL_REGION_MAX_REGIONS);
    expect(regions[0]?.startSec).toBeCloseTo(2, 5);
    expect(regions[regions.length - 1]?.startSec).toBeCloseTo(5, 5);
  });

  it('does not exceed the clip when the last columns are hot', () => {
    const regions = detectCallRegions(clipWithBursts([[90, COLUMNS]]), {
      durationSec: TEN_SECONDS,
    });
    expect(regions).toHaveLength(1);
    expect(regions[0]?.endSec).toBeLessThanOrEqual(TEN_SECONDS);
  });

  it('is stricter than three over the spread, by the shared multiplier', () => {
    expect(CALL_REGION_MAD_MULTIPLIER).toBe(4);
  });
});

describe('bandFromRows', () => {
  // 120 rows for a 12 kHz axis, so a row is 0.1 kHz and index 0 is the top (12 kHz).
  const ROWS = 120;
  const NYQUIST = 12;

  /** Brightness profile with a band of `boost` between the two kHz values. */
  function bandRows(lowKHz: number, highKHz: number, boost = 0.5, rumble = 0.9): Float32Array {
    const rows = new Float32Array(ROWS);
    for (let i = 0; i < ROWS; i++) {
      const khz = ((ROWS - 1 - i) / (ROWS - 1)) * NYQUIST;
      rows[i] = khz < 0.3 ? rumble : 0.01;
      if (khz >= lowKHz && khz <= highKHz) rows[i] += boost;
    }
    return rows;
  }
  const flatBaseline = new Float32Array(ROWS).fill(0);

  it('reports the band the brightness stands in', () => {
    const band = bandFromRows(bandRows(2, 4), flatBaseline, { nyquistKHz: NYQUIST });
    expect(band?.lowKHz).toBeCloseTo(2, 0);
    expect(band?.highKHz).toBeCloseTo(4, 0);
  });

  it('ignores steady rumble that is present all clip', () => {
    const region = bandRows(2, 4, 0.5, 0.9);
    // The same rumble measured over the whole clip is the baseline, not a band.
    const baseline = new Float32Array(ROWS);
    for (let i = 0; i < ROWS; i++) {
      const khz = ((ROWS - 1 - i) / (ROWS - 1)) * NYQUIST;
      baseline[i] = khz < 0.3 ? 0.9 : 0.01;
    }
    const band = bandFromRows(region, baseline, { nyquistKHz: NYQUIST });
    expect(band?.lowKHz).toBeGreaterThanOrEqual(1.5);
    expect(band?.highKHz).toBeLessThanOrEqual(4.5);
  });

  it('refuses a band that is really the whole axis', () => {
    // Bright everywhere except the top two rows: that is "everything is loud",
    // which is not a frequency band and must not be reported as one.
    const plateau = new Float32Array(ROWS).fill(0.5);
    plateau[0] = 0;
    plateau[1] = 0;
    expect(bandFromRows(plateau, flatBaseline, { nyquistKHz: NYQUIST })).toBeNull();
  });

  it('refuses to name a band when nothing stands above the baseline', () => {
    const rows = new Float32Array(ROWS).fill(0.3);
    expect(
      bandFromRows(rows, new Float32Array(ROWS).fill(0.3), { nyquistKHz: NYQUIST })
    ).toBeNull();
  });

  it('refuses to name a band when there is no axis to place it on', () => {
    expect(bandFromRows(bandRows(2, 4), flatBaseline, { nyquistKHz: 0 })).toBeNull();
    expect(
      bandFromRows(new Float32Array(1), new Float32Array(1), { nyquistKHz: NYQUIST })
    ).toBeNull();
  });

  it('scales the band to a bat axis instead of assuming 12 kHz', () => {
    // The same shape near the top of the raster means a far higher frequency when
    // the axis runs to half the capture rate (96 kHz for a 192 kHz clip).
    const rows = new Float32Array(ROWS);
    rows[5] = 0.5;
    const band = bandFromRows(rows, flatBaseline, { nyquistKHz: 96, minKHz: 0 });
    expect(band).not.toBeNull();
    expect(band?.highKHz).toBeGreaterThan(90);
    expect(band?.lowKHz).toBeGreaterThan(85);
  });

  it('keeps the bird band cutoff off the rumble rows', () => {
    expect(BAND_MIN_KHZ).toBe(0.3);
    // A spike in the very bottom row is below the cutoff and must not become the band.
    const rumbleOnly = new Float32Array(ROWS);
    rumbleOnly[ROWS - 1] = 1;
    expect(bandFromRows(rumbleOnly, flatBaseline, { nyquistKHz: NYQUIST })).toBeNull();
  });
});

describe('columnsForSeconds', () => {
  it('maps a span of clip time onto raster columns', () => {
    expect(columnsForSeconds(1, 2, TEN_SECONDS, COLUMNS)).toEqual([10, 20]);
    expect(columnsForSeconds(0, TEN_SECONDS, TEN_SECONDS, COLUMNS)).toEqual([0, COLUMNS]);
  });

  it('always covers at least one column, so a sliver of time is still measurable', () => {
    expect(columnsForSeconds(1, 1, TEN_SECONDS, COLUMNS)).toEqual([10, 11]);
    expect(columnsForSeconds(9.9, 9.9, TEN_SECONDS, COLUMNS)).toEqual([99, 100]);
  });

  it('has no columns when there is no clip', () => {
    expect(columnsForSeconds(1, 2, 0, COLUMNS)).toEqual([0, 1]);
  });
});

describe('rowMedianEnergy', () => {
  it('takes the middle column of each row, so a narrow spike is not the norm', () => {
    // Four columns: three at room tone, one bright. A mean would report the row as lit,
    // which is exactly what would hide the call in the baseline.
    const raster = synthRaster(4, 2, x => (x === 1 ? 200 : 10));
    expect(Array.from(rowMedianEnergy(raster, { x: 0, y: 0, width: 4, height: 2 }))).toEqual([
      10, 10,
    ]);
  });

  it('reads only the rows inside the rectangle', () => {
    const raster = synthRaster(
      3,
      4,
      () => 0,
      y => y
    );
    expect(Array.from(rowMedianEnergy(raster, { x: 0, y: 1, width: 3, height: 2 }))).toEqual([
      1, 2,
    ]);
  });
});

/**
 * A served-looking spectrogram image: full-size frame, dark room tone, and bright
 * rectangles at the given raster columns and rows.
 */
function synthImage(
  bars: { fromColumn: number; toColumn: number; fromRow: number; toRow: number }[],
  width = 1170,
  height = 591
): PixelSource {
  const data = new Uint8ClampedArray(width * height * 4).fill(10);
  const rect = rasterRect(width, height, true);
  if (!rect) throw new Error('synthetic image is smaller than its margins');
  for (const bar of bars) {
    for (let x = rect.x + bar.fromColumn; x < rect.x + bar.toColumn; x++) {
      for (let y = rect.y + bar.fromRow; y < rect.y + bar.toRow; y++) {
        const p = (y * width + x) * 4;
        data[p] = 200;
      }
    }
  }
  return { width, height, data };
}

const TWELVE_SECONDS = 12;

describe('callOverlayFromPixels', () => {
  // One 12 s clip drawn at the detail-page size: 1026 raster columns, 513 rows.
  const RASTER = { columns: 1026, rows: 513 };
  const secondsOf = (column: number) => (column / RASTER.columns) * TWELVE_SECONDS;
  const khzOf = (row: number) => ((RASTER.rows - 1 - row) / (RASTER.rows - 1)) * BIRD_NYQUIST_KHZ;

  it('reports where in the clip the sound is, and in which band', () => {
    const bar = { fromColumn: 102, toColumn: 205, fromRow: 200, toRow: 300 };
    const overlay = callOverlayFromPixels({
      pixels: synthImage([bar]),
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    if (!overlay) throw new Error('a drawn call should produce an overlay');
    expect(overlay.regions).toHaveLength(1);
    const region = overlay.regions.find(item => item.primary);
    if (!region) throw new Error('the only region should be the primary one');
    expect(region.startSec).toBeCloseTo(secondsOf(bar.fromColumn), 2);
    expect(region.endSec).toBeCloseTo(secondsOf(bar.toColumn), 2);
    expect(region.primary).toBe(true);
    if (!overlay.band) throw new Error('a drawn band should be reported');
    // The bar was drawn between these two rows, so the band is their edges.
    expect(overlay.band.lowKHz).toBeCloseTo(khzOf(bar.toRow - 1), 1);
    expect(overlay.band.highKHz).toBeCloseTo(khzOf(bar.fromRow), 1);
  });

  it('gives each of several calls its own span', () => {
    const overlay = callOverlayFromPixels({
      pixels: synthImage([
        { fromColumn: 100, toColumn: 140, fromRow: 100, toRow: 160 },
        { fromColumn: 700, toColumn: 760, fromRow: 220, toRow: 300 },
      ]),
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    expect(overlay?.regions.map(region => region.startSec)).toHaveLength(2);
    expect(overlay?.regions[1]?.startSec).toBeGreaterThan(overlay?.regions[0]?.endSec ?? 0);
  });

  it('draws a band for the strongest call only', () => {
    const overlay = callOverlayFromPixels({
      pixels: synthImage([
        { fromColumn: 100, toColumn: 140, fromRow: 100, toRow: 160 }, // 4-7 kHz
        { fromColumn: 700, toColumn: 760, fromRow: 400, toRow: 500 }, // near the bottom
      ]),
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    const primary = overlay?.regions.find(region => region.primary);
    if (!primary || !overlay?.band) throw new Error('expected a primary call with a band');
    // The wide low bar covers more rows, so it is the strongest: the band must be its rows.
    expect(overlay.band.highKHz).toBeLessThan(BIRD_NYQUIST_KHZ / 2);
  });

  it('reports nothing to draw when the image never varies', () => {
    const flat = { width: 1170, height: 591, data: new Uint8ClampedArray(1170 * 591 * 4).fill(90) };
    const overlay = callOverlayFromPixels({
      pixels: flat,
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    expect(overlay?.regions).toEqual([]);
    expect(overlay?.band).toBeNull();
  });

  it('reports nothing to draw when there is no clip length to place time on', () => {
    const overlay = callOverlayFromPixels({
      pixels: synthImage([{ fromColumn: 100, toColumn: 200, fromRow: 100, toRow: 200 }]),
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: 0,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    expect(overlay?.regions).toEqual([]);
    expect(overlay?.band).toBeNull();
  });

  it('refuses to guess when the pixels are missing or the image has no plot area', () => {
    const base = {
      width: 1170,
      height: 591,
      axesDrawn: true,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    };
    expect(callOverlayFromPixels({ ...base, pixels: null })).toBeNull();
    expect(
      callOverlayFromPixels({
        ...base,
        width: 144,
        height: 78,
        pixels: { width: 144, height: 78, data: new Uint8ClampedArray(144 * 78 * 4) },
      })
    ).toBeNull();
  });

  it('treats a raw image as all plot area', () => {
    const overlay = callOverlayFromPixels({
      pixels: (() => {
        const data = new Uint8ClampedArray(1026 * 513 * 4).fill(10);
        for (let x = 100; x < 200; x++) {
          for (let y = 200; y < 300; y++) data[(y * 1026 + x) * 4] = 200;
        }
        return { width: 1026, height: 513, data };
      })(),
      width: 1026,
      height: 513,
      axesDrawn: false,
      durationSec: TWELVE_SECONDS,
      nyquistKHz: BIRD_NYQUIST_KHZ,
    });
    if (!overlay) throw new Error('a raw image should still be measurable');
    expect(overlay.rect).toEqual({ x: 0, y: 0, width: 1026, height: 513 });
    expect(overlay.regions[0]?.startSec).toBeCloseTo((100 / 1026) * TWELVE_SECONDS, 2);
  });
});

describe('overlayBox', () => {
  const overlay = callOverlayFromPixels({
    // Drawn at the list-page size: a 514x257 image holds a 370x179 raster.
    pixels: synthImage([{ fromColumn: 102, toColumn: 205, fromRow: 40, toRow: 90 }], 514, 257),
    width: 514,
    height: 257,
    axesDrawn: true,
    durationSec: TWELVE_SECONDS,
    nyquistKHz: BIRD_NYQUIST_KHZ,
  });

  it('expresses image pixels as percentages of the image box', () => {
    if (!overlay) throw new Error('expected an overlay to position');
    expect(overlay.regions).toHaveLength(1);
    const box = overlayBox(overlay, 58 + 16, 30, 4, 100);
    expect(box?.leftPercent).toBeCloseTo((74 / 514) * 100, 4);
    expect(box?.topPercent).toBeCloseTo((30 / 257) * 100, 4);
    expect(box?.widthPercent).toBeCloseTo((4 / 514) * 100, 4);
    expect(box?.heightPercent).toBeCloseTo((100 / 257) * 100, 4);
  });

  it('has no percentages when the box has no size', () => {
    if (!overlay) throw new Error('expected an overlay to position');
    expect(
      overlayBox({ ...overlay, imageSize: { width: 0, height: 257 } }, 60, 30, 4, 4)
    ).toBeNull();
    expect(overlayBox(overlay, 60, 30, 0, 4)).toBeNull();
  });
});

describe('frequencyBottomPercent', () => {
  // The detail-page image measured off the server: 1170x591 holding a 1026x513 raster,
  // so the raster starts 48 px above the bottom edge and ends 30 px below the top.
  const painted = { width: 1170, height: 591, axesDrawn: true };
  const raw = { width: 1026, height: 513, axesDrawn: false };

  it('places 0 Hz and the top of the axis on the raster, not on the image edges', () => {
    expect(frequencyBottomPercent(0, 12, painted)).toBeCloseTo((48 / 591) * 100, 4);
    expect(frequencyBottomPercent(12, 12, painted)).toBeCloseTo(((48 + 513) / 591) * 100, 4);
    expect(frequencyBottomPercent(6, 12, painted)).toBeCloseTo(((48 + 256.5) / 591) * 100, 4);
  });

  it('needs no correction on a raw image, which is all raster', () => {
    expect(frequencyBottomPercent(0, 12, raw)).toBeCloseTo(0, 4);
    expect(frequencyBottomPercent(12, 12, raw)).toBeCloseTo(100, 4);
    expect(frequencyBottomPercent(6, 12, raw)).toBeCloseTo(50, 4);
  });

  it('differs from the naive image-box fraction by more than a rounding error', () => {
    // A label at kHz/nyquist of the whole box lands ~7 points low on this image, which is
    // why the axis labels and the overlay share this mapping instead of each doing maths.
    const naive = (1 / 12) * 100;
    const corrected = frequencyBottomPercent(1, 12, painted) ?? 0;
    expect(Math.abs(corrected - naive)).toBeGreaterThan(5);
  });

  it('keeps out-of-range values on the axis and refuses an unusable one', () => {
    expect(frequencyBottomPercent(20, 12, painted)).toBeCloseTo(((48 + 513) / 591) * 100, 4);
    expect(frequencyBottomPercent(-3, 12, painted)).toBeCloseTo((48 / 591) * 100, 4);
    expect(frequencyBottomPercent(4, 0, painted)).toBeNull();
    expect(frequencyBottomPercent(Number.NaN, 12, painted)).toBeNull();
    expect(frequencyBottomPercent(4, 12, { width: 100, height: 50, axesDrawn: true })).toBeNull();
  });
});

describe('timeLeftPercent', () => {
  const painted = { width: 1170, height: 591, axesDrawn: true };

  it('maps the clip onto the raster between the painted axes', () => {
    expect(timeLeftPercent(0, 12, painted)).toBeCloseTo((58 / 1170) * 100, 4);
    expect(timeLeftPercent(12, 12, painted)).toBeCloseTo(((58 + 1026) / 1170) * 100, 4);
    expect(timeLeftPercent(3, 12, painted)).toBeCloseTo(((58 + 256.5) / 1170) * 100, 4);
  });

  it('stays inside the raster for times outside the clip', () => {
    expect(timeLeftPercent(-5, 12, painted)).toBeCloseTo((58 / 1170) * 100, 4);
    expect(timeLeftPercent(60, 12, painted)).toBeCloseTo(((58 + 1026) / 1170) * 100, 4);
  });

  it('has no position when the clip has no length', () => {
    expect(timeLeftPercent(3, 0, painted)).toBeNull();
    expect(timeLeftPercent(Number.NaN, 12, painted)).toBeNull();
  });
});

// Everything below runs against bytes served by the live instance, so a regression here
// means the overlay stopped matching what a person can see and hear on a real recording.
describe('a real served recording (winter wren)', () => {
  it('maps the served list-page image onto the painted raster', () => {
    expect(winterWrenClip.columnProfile).toHaveLength(winterWrenClip.rasterColumns);
    expect(winterWrenClip.regionRowProfile).toHaveLength(winterWrenClip.rasterRows);
    expect(
      rasterRect(winterWrenClip.imageWidth, winterWrenClip.imageHeight, winterWrenClip.axesDrawn)
    ).toEqual({
      x: 58,
      y: 30,
      width: winterWrenClip.rasterColumns,
      height: winterWrenClip.rasterRows,
    });
  });

  it('places the calls where the bird is, across a minute of wind', () => {
    const regions = detectCallRegions(Float32Array.from(winterWrenClip.columnProfile), {
      durationSec: winterWrenClip.clipSeconds,
    });
    // One comparison for the whole list instead of one per field: when this fails, the
    // diff between what was measured and what the clip contains is the answer. Compared to
    // a tenth of a second — a sixth of a percent of this clip, far finer than the shading
    // is read at, and coarse enough that the fixture's own rounding is not the failure.
    const tenth = (seconds: number) => Math.round(seconds * 10) / 10;
    expect(
      regions.map(region => ({
        startSec: tenth(region.startSec),
        endSec: tenth(region.endSec),
        primary: region.primary,
      }))
    ).toEqual(
      winterWrenClip.expectedRegions.map(expected => ({
        startSec: tenth(expected.startSec),
        endSec: tenth(expected.endSec),
        primary: expected.primary,
      }))
    );
    for (const region of regions) {
      expect(region.endSec).toBeGreaterThan(region.startSec);
      expect(region.endSec).toBeLessThanOrEqual(winterWrenClip.clipSeconds);
    }
  });

  it('reports the band the bird actually sings in, not the wind rumble', () => {
    const band = bandFromRows(
      Float32Array.from(winterWrenClip.regionRowProfile),
      Float32Array.from(winterWrenClip.clipRowBaseline),
      { nyquistKHz: BIRD_NYQUIST_KHZ }
    );
    if (!band) throw new Error('the wren phrase should yield a frequency band');
    expect(band.lowKHz).toBeCloseTo(winterWrenClip.expectedBand.lowKHz, 2);
    expect(band.highKHz).toBeCloseTo(winterWrenClip.expectedBand.highKHz, 2);
    // A band is only useful to the overlay if it sits inside the drawn axis.
    expect(band.lowKHz).toBeGreaterThanOrEqual(BAND_MIN_KHZ);
    expect(band.highKHz).toBeLessThanOrEqual(BIRD_NYQUIST_KHZ);
  });
});
