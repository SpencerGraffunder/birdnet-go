/**
 * Example-call lookup for the detection detail panel.
 *
 * The backend serves a short published recording of the species from its own
 * cache and answers one of three things: here it is (200 with metadata), not
 * yet, we are fetching it (503 with Retry-After), or there is none (404). This
 * module turns that into a small discriminated result so the component only has
 * to render a state instead of parsing HTTP.
 *
 * It deliberately does not treat 503 as an error: an example call that is still
 * being fetched is expected on a species' first sighting, and the caller is
 * supposed to try again shortly rather than show a failure.
 */

import { buildAppUrl } from '$lib/utils/urlHelpers';

/** Metadata the backend publishes for one cached example call. */
export interface SpeciesSoundInfo {
  state: string;
  scientificName?: string;
  audioURL?: string;
  mimeType?: string;
  durationSeconds?: number;
  recordist?: string;
  licenseName?: string;
  licenseUrl?: string;
  pageUrl?: string;
  recordingId?: string;
  source?: string;
  trimmed?: boolean;
  originalSeconds?: number;
}

export type SpeciesSoundResult =
  | { kind: 'ready'; info: SpeciesSoundInfo }
  /** Being fetched upstream; ask again after `retryAfterSeconds`. */
  | { kind: 'pending'; retryAfterSeconds: number }
  /** Xeno-Canto has no recording for this species, or the feature is off. */
  | { kind: 'unavailable' }
  /** The request itself failed (network, server). */
  | { kind: 'error' };

/** Where the metadata for one species' example call lives. */
export function speciesSoundInfoUrl(scientificName: string): string {
  return buildAppUrl(
    `/api/v2/media/species-sound/${encodeURIComponent(scientificName.trim())}/info`
  );
}

/** Used when a fetch has to be retried after the backend's Retry-After hint. */
export const DEFAULT_RETRY_AFTER_SECONDS = 5;

/**
 * Parse a Retry-After header, which the backend sends in seconds. Anything that
 * is not a sane number of seconds falls back to the default so a malformed or
 * enormous value cannot park a request for minutes.
 */
export function parseRetryAfter(value: string | null): number {
  const seconds = Number(value);
  if (!Number.isFinite(seconds) || seconds <= 0 || seconds > 60) {
    return DEFAULT_RETRY_AFTER_SECONDS;
  }
  return seconds;
}

/**
 * Ask what is known about a species' example call. Never throws: every failure
 * mode becomes a result kind, because the caller renders all four of them.
 */
export async function loadSpeciesSound(
  scientificName: string,
  fetchImpl: typeof fetch = fetch
): Promise<SpeciesSoundResult> {
  const name = scientificName.trim();
  if (!name) {
    return { kind: 'unavailable' };
  }

  let response: Response;
  try {
    response = await fetchImpl(speciesSoundInfoUrl(name), {
      method: 'GET',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
  } catch {
    // Offline, DNS failure, aborted navigation: nothing to show, but not a bug.
    return { kind: 'error' };
  }

  if (response.ok) {
    const info = (await response.json().catch(() => null)) as SpeciesSoundInfo | null;
    if (!info || typeof info.audioURL !== 'string' || info.audioURL === '') {
      return { kind: 'error' };
    }
    return { kind: 'ready', info };
  }

  if (response.status === 404) {
    return { kind: 'unavailable' };
  }
  if (response.status === 503) {
    // The backend sends Retry-After only when it is actually fetching a clip. A
    // 503 without it means the feature is switched off, which no amount of
    // waiting will change, so it is reported as unavailable rather than pending.
    const retryAfter = response.headers.get('Retry-After');
    if (retryAfter === null || retryAfter.trim() === '') {
      return { kind: 'unavailable' };
    }
    return {
      kind: 'pending',
      retryAfterSeconds: parseRetryAfter(retryAfter),
    };
  }
  return { kind: 'error' };
}

/**
 * Build the attribution shown in the play button's tooltip.
 *
 * Xeno-Canto's licence terms are the recordist's choice and some of them
 * require credit, so the credit is always shown when the backend has it. The
 * text degrades instead of breaking when a field is missing.
 */
export function describeSpeciesSoundAttribution(
  info: SpeciesSoundInfo | null,
  labels: {
    withLicense: (recordist: string, license: string) => string;
    withoutLicense: (recordist: string) => string;
    unknown: () => string;
  }
): string {
  const recordist = info?.recordist?.trim() ?? '';
  const license = info?.licenseName?.trim() ?? '';
  if (!recordist) {
    return labels.unknown();
  }
  return license ? labels.withLicense(recordist, license) : labels.withoutLicense(recordist);
}

/** True when a stored clip was shortened from a longer published recording. */
export function isTrimmedClip(info: SpeciesSoundInfo | null): boolean {
  return Boolean(info?.trimmed);
}
