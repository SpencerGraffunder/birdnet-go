/**
 * callOverlay.ts
 *
 * Where in a detection clip the sound actually is, measured from the spectrogram the
 * player is already showing, and the geometry that turns that into an overlay.
 *
 * Why measured rather than stored: BirdNET scores a whole analysis window and the
 * pipeline keeps one confidence per detection, so a stored detection carries no
 * position. `endTime - beginTime` looks like a position but is not one —
 * `normalizeDetectionTimes` backdates `BeginTime` to the first hit and sets `EndTime`
 * to `FirstDetected + normalDetectionWindow`, so the span is the configured capture
 * window (a constant), and the extra pre-capture padding is appended after it because
 * the capture reads `[BeginTime, BeginTime + length]`. Highlighting the stored span
 * would therefore paint the same fraction of every clip and say nothing about the
 * bird. The spectrogram, in contrast, is a dB-scaled picture of exactly this clip: one
 * column is one instant, one row is one frequency band. Measuring it answers "where is
 * the energy" with the same evidence the eye uses, needs no extra download, and works
 * for clips recorded before this existed.
 *
 * Everything here is pure arithmetic over a small grey raster so the decisions are
 * unit-testable without a canvas; the caller's only browser work is drawing the image
 * to a canvas and handing over its pixels.
 *
 * The axis facts come from the renderer, not from guessing: `soxchrome/layout.go`
 * fixes the margins, the frequency axis is linear with 0 Hz at the raster bottom
 * (`FrequencyProfile` gives the top of the axis: 12 kHz for birds, half the capture
 * rate for bats), and both were confirmed against a served clip (per-row brightness
 * tracks a Welch PSD of the same audio at r = +0.998).
 */

/**
 * Pixels the server paints around the raster, mirroring the constants in
 * `internal/spectrogram/soxchrome/layout.go` (`MarginLeft`, `MarginAbove`,
 * `MarginBelow`, and the right-hand block `GapBetween` + `LegendWidth` +
 * `MarginRight`). They are fixed pixel counts, independent of the `-X` raster size,
 * so the raster can be recovered from the image size alone. Keep in step with Go.
 */
export const SPECTROGRAM_IMAGE_MARGINS = {
  left: 58,
  above: 30,
  below: 48,
  right: 86,
} as const;

/** A rectangle in image pixels. */
export interface RasterRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/**
 * The plot area of a spectrogram image, in image pixels, or null when the image is too
 * small to hold the painted margins (a corrupt or unexpected file).
 *
 * `axesDrawn` is whether the image carries the server-painted axis chrome: a raw image
 * is the raster alone, so the plot area is the whole image.
 */
export function rasterRect(width: number, height: number, axesDrawn: boolean): RasterRect | null {
  if (!Number.isFinite(width) || !Number.isFinite(height)) return null;
  if (!axesDrawn) {
    return width > 0 && height > 0 ? { x: 0, y: 0, width, height } : null;
  }
  const plotWidth =
    Math.floor(width) - SPECTROGRAM_IMAGE_MARGINS.left - SPECTROGRAM_IMAGE_MARGINS.right;
  const plotHeight =
    Math.floor(height) - SPECTROGRAM_IMAGE_MARGINS.above - SPECTROGRAM_IMAGE_MARGINS.below;
  if (plotWidth <= 0 || plotHeight <= 0) return null;
  return {
    x: SPECTROGRAM_IMAGE_MARGINS.left,
    y: SPECTROGRAM_IMAGE_MARGINS.above,
    width: plotWidth,
    height: plotHeight,
  };
}

/** A grey image: `values` is row-major, length `width * height`, each entry 0..1. */
export interface GreyRaster {
  width: number;
  height: number;
  values: Float32Array;
}

/** The shape of a canvas `ImageData` (or a test's stand-in) that this module reads. */
export interface PixelSource {
  width: number;
  height: number;
  data: ArrayLike<number>;
}

/**
 * Reduce RGBA pixels to grey, keeping the brightest channel of each pixel.
 *
 * The spectrogram palette encodes level in brightness, so the channel maximum tracks
 * it without inverting the colour table. The alpha channel is ignored: the server
 * writes an opaque image. Returns null for input that cannot describe an image, so a
 * failed canvas read simply produces no overlay.
 */
export function greyRasterFromPixels(pixels: PixelSource): GreyRaster | null {
  const { width, height, data } = pixels;
  if (!Number.isFinite(width) || !Number.isFinite(height)) return null;
  if (width <= 0 || height <= 0 || data.length < width * height * 4) return null;
  const values = new Float32Array(width * height);
  for (let i = 0, p = 0; i < values.length; i++, p += 4) {
    const r = data[p] ?? 0;
    const g = data[p + 1] ?? 0;
    const b = data[p + 2] ?? 0;
    values[i] = Math.max(r, g, b) / 255;
  }
  return { width, height, values };
}

/** Mean brightness of each column inside the rectangle, left to right: the clip's energy over time. */
export function columnEnergy(raster: GreyRaster, rect: RasterRect): Float32Array {
  const out = new Float32Array(Math.max(0, Math.floor(rect.width)));
  const xEnd = Math.min(raster.width, rect.x + rect.width);
  const yEnd = Math.min(raster.height, rect.y + rect.height);
  for (let col = 0; col < out.length; col++) {
    const x = rect.x + col;
    if (x >= xEnd) break;
    let sum = 0;
    for (let y = rect.y; y < yEnd; y++) sum += raster.values[y * raster.width + x];
    out[col] = yEnd > rect.y ? sum / (yEnd - rect.y) : 0;
  }
  return out;
}

/**
 * Mean brightness of each row between two columns, top of the raster first, so index 0
 * is the highest frequency. Rows outside the rectangle are ignored.
 */
export function rowEnergy(
  raster: GreyRaster,
  rect: RasterRect,
  fromColumn: number,
  toColumn: number
): Float32Array {
  const out = new Float32Array(Math.max(0, Math.floor(rect.height)));
  const first = Math.max(0, fromColumn);
  const last = Math.min(rect.width, Math.max(fromColumn + 1, toColumn));
  const count = last - first;
  if (count <= 0) return out;
  const xEnd = Math.min(raster.width, rect.x + rect.width);
  const yEnd = Math.min(raster.height, rect.y + rect.height);
  for (let row = 0; row < out.length; row++) {
    const y = rect.y + row;
    if (y >= yEnd) break;
    let sum = 0;
    for (let x = rect.x + first; x < rect.x + last && x < xEnd; x++) {
      sum += raster.values[y * raster.width + x];
    }
    out[row] = sum / count;
  }
  return out;
}

/** Median of a profile, or 0 when it holds nothing. */
export function median(values: ArrayLike<number>): number {
  if (values.length === 0) return 0;
  const sorted = Float32Array.from(values).sort();
  const mid = sorted.length >> 1;
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

/** Median absolute deviation of a profile around `centre`, the robust spread used to threshold bursts. */
export function medianAbsoluteDeviation(
  values: ArrayLike<number>,
  centre: number = median(values)
): number {
  if (values.length === 0) return 0;
  const deltas = new Float32Array(values.length);
  for (let i = 0; i < values.length; i++) deltas[i] = Math.abs(values[i] - centre);
  return median(deltas);
}

/** A span of the clip, in seconds, where sound energy stands out. */
export interface CallRegion {
  startSec: number;
  endSec: number;
  /** How far the region's brightest column stands above the clip's noise floor. */
  prominence: number;
  /** True for the region a listener is most likely after (the strongest one). */
  primary: boolean;
}

/** One over the median absolute deviation; a flat clip has no measurable spread. */
const MIN_SPREAD = 1e-6;

/** Columns brighter than median + this × MAD count as sound rather than room tone. */
export const CALL_REGION_MAD_MULTIPLIER = 4;

/** Gaps shorter than this are inside one call, so the bursts around them are merged. */
export const CALL_REGION_MERGE_GAP_SECONDS = 0.15;

/** Shorter than this and it is a click, not a call worth a highlight. */
export const CALL_REGION_MIN_LENGTH_SECONDS = 0.2;

/** Enough highlights to read a clip without wallpapering a 200 px thumbnail. */
export const CALL_REGION_MAX_REGIONS = 4;

/** Options for {@link detectCallRegions}, each defaulting to the constant above. */
export interface CallRegionOptions {
  /** Clip length in seconds; regions are reported against it. */
  durationSec: number;
  madMultiplier?: number;
  mergeGapSec?: number;
  minLengthSec?: number;
  maxRegions?: number;
}

function toColumnRange(seconds: number, durationSec: number, columns: number): number {
  if (durationSec <= 0) return 0;
  return Math.min(columns, Math.max(0, Math.round((seconds / durationSec) * columns)));
}

/**
 * Contiguous stretches whose mean brightness stands out from the clip's own noise
 * floor, ordered by time and capped to the strongest few.
 *
 * The threshold is median + k × MAD rather than a mean and a standard deviation
 * because a clip is mostly quiet and a handful of loud columns would drag a mean-based
 * threshold up until only the loudest call survives — the opposite of finding where
 * the bird is. The floor is the clip's own, so a windy recording and a still one are
 * judged against their own room tone.
 *
 * A clip with no measurable spread (digital silence, or an image that never varied)
 * yields no regions rather than a guess.
 */
export function detectCallRegions(
  columns: ArrayLike<number>,
  options: CallRegionOptions
): CallRegion[] {
  const { durationSec } = options;
  if (!Number.isFinite(durationSec) || durationSec <= 0 || columns.length < 2) return [];
  const floor = median(columns);
  const spread = Math.max(medianAbsoluteDeviation(columns, floor), MIN_SPREAD);
  const multiplier = options.madMultiplier ?? CALL_REGION_MAD_MULTIPLIER;
  const threshold = floor + multiplier * spread;

  const mergeGapColumns = Math.max(
    1,
    toColumnRange(options.mergeGapSec ?? CALL_REGION_MERGE_GAP_SECONDS, durationSec, columns.length)
  );
  const minLengthColumns = Math.max(
    1,
    toColumnRange(
      options.minLengthSec ?? CALL_REGION_MIN_LENGTH_SECONDS,
      durationSec,
      columns.length
    )
  );

  interface Run {
    start: number;
    end: number;
    peak: number;
  }
  const runs: Run[] = [];
  let current: Run | null = null;
  let quietGap = 0;
  for (let i = 0; i < columns.length; i++) {
    const hot = columns[i] > threshold;
    if (hot) {
      const prominence = columns[i] - floor;
      if (current) {
        current.end = i + 1;
        current.peak = Math.max(current.peak, prominence);
      } else {
        current = { start: i, end: i + 1, peak: prominence };
      }
      quietGap = 0;
    } else if (current) {
      quietGap++;
      if (quietGap > mergeGapColumns) {
        runs.push(current);
        current = null;
        quietGap = 0;
      }
    }
  }
  if (current) runs.push(current);

  const kept = runs.filter(run => run.end - run.start >= minLengthColumns);
  const maxRegions = Math.max(1, Math.trunc(options.maxRegions ?? CALL_REGION_MAX_REGIONS));
  const strongest = [...kept].sort((a, b) => b.peak - a.peak).slice(0, maxRegions);
  const primary = strongest[0];

  return strongest
    .sort((a, b) => a.start - b.start)
    .map(run => ({
      startSec: (run.start / columns.length) * durationSec,
      endSec: (run.end / columns.length) * durationSec,
      prominence: run.peak,
      primary: run === primary,
    }));
}

/** A band of the frequency axis, in kHz. */
export interface FrequencyBand {
  lowKHz: number;
  highKHz: number;
}

/** Below this the axis is wind rumble and transformer hum, not birdsong. */
export const BAND_MIN_KHZ = 0.3;

/** A band filling more than this share of the axis is "everything", which is not a band. */
export const BAND_MAX_COVERAGE = 0.85;

/** Rows at least this share of the region's brightest row belong to the band. */
export const BAND_LEVEL_FRACTION = 0.5;

/** Options for {@link bandFromRows}. */
export interface BandOptions {
  /** kHz at the top of the raster: 12 for birds, half the capture rate for bats. */
  nyquistKHz: number;
  minKHz?: number;
  maxCoverage?: number;
  levelFraction?: number;
}

/**
 * The frequency band a time region sits in, from its per-row brightness.
 *
 * `baseline` is the same row profile measured over the whole clip. Subtracting it per
 * row removes what is always there — wind, traffic, an electrical whine — which
 * otherwise dominates the total energy and pins every answer to the bottom of the
 * axis. What survives is what is specific to this moment: the call.
 *
 * Returns null when there is nothing to report: no usable axis, no positive
 * brightness above the baseline, or a band wide enough that it would be the whole
 * axis. Callers show shading only when this returns a band, so an uninformative clip
 * stays unannotated instead of getting a confident number.
 */
export function bandFromRows(
  rows: ArrayLike<number>,
  baseline: ArrayLike<number>,
  options: BandOptions
): FrequencyBand | null {
  const nyquist = options.nyquistKHz;
  if (!Number.isFinite(nyquist) || nyquist <= 0 || rows.length < 2) return null;
  const minKHz = Math.max(0, options.minKHz ?? BAND_MIN_KHZ);
  const levelFraction = options.levelFraction ?? BAND_LEVEL_FRACTION;
  const count = Math.min(rows.length, baseline.length);

  // Index 0 is the top of the raster = the nyquist, so a row's kHz falls as the index
  // grows: the bird band (at or above the cutoff) is the run of rows 0..limit.
  const khzOf = (index: number) => ((count - 1 - index) / (count - 1)) * nyquist;
  const halfRowKHz = nyquist / (count - 1) / 2;
  const limit =
    minKHz <= 0
      ? count - 1
      : Math.max(0, Math.min(count - 1, Math.floor((count - 1) * (1 - minKHz / nyquist))));

  let peakValue = 0;
  let peakIndex = -1;
  for (let i = 0; i <= limit; i++) {
    const above = rows[i] - (baseline[i] ?? 0);
    if (above > peakValue) {
      peakValue = above;
      peakIndex = i;
    }
  }
  if (peakIndex < 0 || peakValue <= 0) return null;

  const needed = peakValue * levelFraction;
  let low = peakIndex; // walking towards the bottom of the raster, so towards lower kHz
  while (low + 1 <= limit && rows[low + 1] - (baseline[low + 1] ?? 0) >= needed) low++;
  let high = peakIndex;
  while (high - 1 >= 0 && rows[high - 1] - (baseline[high - 1] ?? 0) >= needed) high--;

  // A band covers the bins it occupies, so it reaches half a row past each end;
  // otherwise a single hot row — a tonal call — would report no width at all.
  const lowKHz = Math.max(minKHz, khzOf(low) - halfRowKHz);
  const highKHz = Math.min(nyquist, khzOf(high) + halfRowKHz);
  if (highKHz <= lowKHz) return null;
  const span = nyquist - minKHz;
  if (span > 0 && (highKHz - lowKHz) / span > (options.maxCoverage ?? BAND_MAX_COVERAGE)) {
    return null;
  }
  return { lowKHz, highKHz };
}

/** The columns of a raster that a span of clip time covers, for {@link rowEnergy}. */
export function columnsForSeconds(
  startSec: number,
  endSec: number,
  durationSec: number,
  columns: number
): [number, number] {
  const from = toColumnRange(startSec, durationSec, columns);
  const to = toColumnRange(endSec, durationSec, columns);
  return to > from ? [from, to] : [from, from + 1];
}

/**
 * Median brightness of each row across the whole plot area: what a frequency band looks
 * like in this clip when the call is not happening. Subtracting it from a region's row
 * profile is what turns "this row is bright" into "this row is bright *during the call*",
 * which is the difference between naming the bird's band and naming the wind.
 */
export function rowMedianEnergy(raster: GreyRaster, rect: RasterRect): Float32Array {
  const out = new Float32Array(Math.max(0, Math.floor(rect.height)));
  const xEnd = Math.min(raster.width, rect.x + rect.width);
  const yEnd = Math.min(raster.height, rect.y + rect.height);
  const row = new Float32Array(Math.max(0, xEnd - rect.x));
  for (let index = 0; index < out.length; index++) {
    const y = rect.y + index;
    if (y >= yEnd) break;
    for (let x = rect.x; x < xEnd; x++) row[x - rect.x] = raster.values[y * raster.width + x];
    out[index] = median(row.subarray(0, xEnd - rect.x));
  }
  return out;
}

/** What the overlay needs to draw, all of it relative to the image box. */
export interface CallOverlay {
  /** The plot area inside the image, in image pixels. */
  rect: RasterRect;
  /** Image pixel box of the whole image, so `rect` can be turned into percentages. */
  imageSize: { width: number; height: number };
  /** Where the calls are, in clip time. */
  regions: CallRegion[];
  /** Frequency band of the strongest call, or null when it would be a guess. */
  band: FrequencyBand | null;
}

/** Options for {@link callOverlayFromPixels}. */
export interface CallOverlayOptions {
  /** Pixels of the drawn spectrogram image (its natural size), or null if the read failed. */
  pixels: PixelSource | null;
  /** Natural size of the image those pixels belong to. */
  width: number;
  height: number;
  /** Whether the image carries the server-painted axis chrome. */
  axesDrawn: boolean;
  /** Clip length in seconds, from the audio element. */
  durationSec: number;
  /** kHz at the top of the raster: 12 for birds, half the capture rate for bats. */
  nyquistKHz: number;
}

/**
 * Everything the call overlay draws, from the pixels of the spectrogram the player has
 * already loaded. Returns null when there is nothing trustworthy to draw: unreadable
 * pixels, an image too small for its own margins, or no clip length to place time on.
 *
 * A caller that gets null shows the spectrogram unannotated, which is the honest
 * fallback: an overlay that is merely approximately right would mislead about where in
 * a recording to listen.
 */
export function callOverlayFromPixels(options: CallOverlayOptions): CallOverlay | null {
  const rect = rasterRect(options.width, options.height, options.axesDrawn);
  if (!rect) return null;
  if (!options.pixels) return null;
  const raster = greyRasterFromPixels(options.pixels);
  if (!raster) return null;

  const columns = columnEnergy(raster, rect);
  const regions = detectCallRegions(columns, { durationSec: options.durationSec });
  const imageSize = { width: options.width, height: options.height };
  // The detector marks one region primary whenever it found any, so this lookup is the
  // whole selection rather than a preference with a fallback.
  const primary = regions.find(region => region.primary);
  if (!primary) return { rect, imageSize, regions, band: null };

  const [from, to] = columnsForSeconds(
    primary.startSec,
    primary.endSec,
    options.durationSec,
    columns.length
  );
  const band = bandFromRows(rowEnergy(raster, rect, from, to), rowMedianEnergy(raster, rect), {
    nyquistKHz: options.nyquistKHz,
  });
  return { rect, imageSize, regions, band };
}

/** Percentages of the image box, which is how a DOM overlay is positioned. */
export interface CallOverlayBox {
  leftPercent: number;
  topPercent: number;
  widthPercent: number;
  heightPercent: number;
}

/**
 * A rectangle of the image expressed as percentages of the image box, so an overlay can
 * be positioned with `left/top/width/height` styles and stay aligned at any rendered size.
 * Returns null when the image box has no size to divide by.
 */
export function overlayBox(
  overlay: CallOverlay,
  x: number,
  y: number,
  width: number,
  height: number
): CallOverlayBox | null {
  const boxWidth = overlay.imageSize.width;
  const boxHeight = overlay.imageSize.height;
  if (!(boxWidth > 0) || !(boxHeight > 0)) return null;
  if (!(width > 0) || !(height > 0)) return null;
  return {
    leftPercent: (x / boxWidth) * 100,
    topPercent: (y / boxHeight) * 100,
    widthPercent: (width / boxWidth) * 100,
    heightPercent: (height / boxHeight) * 100,
  };
}

/**
 * The spectrogram a player is showing: its natural pixel size (the size the raster was
 * recovered from) and whether it carries the server-painted axis chrome.
 */
export interface SpectrogramImageGeometry {
  width: number;
  height: number;
  axesDrawn: boolean;
}

/**
 * Where a frequency belongs on the drawn image, as a percentage up from its bottom edge.
 *
 * This is the one place that knows how a kHz value maps to the DOM box, and the frequency
 * labels share it with the call overlay: on an image with painted axes the raster does not
 * reach the edges of the image, so a label placed at `kHz / nyquist` of the whole box sits
 * visibly off the tick it names. Returns null when the image or axis is unusable, so a
 * caller omits the label rather than mislabelling the picture.
 */
export function frequencyBottomPercent(
  khz: number,
  nyquistKHz: number,
  geometry: SpectrogramImageGeometry
): number | null {
  const rect = rasterRect(geometry.width, geometry.height, geometry.axesDrawn);
  if (!rect || !(nyquistKHz > 0) || !Number.isFinite(khz)) return null;
  const fraction = Math.min(1, Math.max(0, khz / nyquistKHz));
  const fromBottom = geometry.height - rect.y - rect.height + fraction * rect.height;
  return (fromBottom / geometry.height) * 100;
}

/**
 * Where a moment of the clip belongs on the drawn image, as a percentage in from its left
 * edge. Null when the image has no plot area or the clip has no length.
 */
export function timeLeftPercent(
  seconds: number,
  durationSec: number,
  geometry: SpectrogramImageGeometry
): number | null {
  const rect = rasterRect(geometry.width, geometry.height, geometry.axesDrawn);
  if (!rect || !(durationSec > 0) || !Number.isFinite(seconds)) return null;
  const fraction = Math.min(1, Math.max(0, seconds / durationSec));
  return ((rect.x + fraction * rect.width) / geometry.width) * 100;
}
