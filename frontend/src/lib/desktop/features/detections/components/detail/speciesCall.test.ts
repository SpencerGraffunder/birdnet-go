import { describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_RETRY_AFTER_SECONDS,
  describeSpeciesSoundAttribution,
  loadSpeciesSound,
  parseRetryAfter,
  speciesSoundInfoUrl,
} from './speciesCall';

/** Minimal Response for the cases the module branches on. */
function response(
  init: { status?: number; retryAfter?: string | null; body?: unknown } = {}
): Response {
  const { status = 200, retryAfter, body = {} } = init;
  const headers = new Headers();
  if (retryAfter !== undefined && retryAfter !== null) {
    headers.set('Retry-After', retryAfter);
  }
  return {
    ok: status >= 200 && status < 300,
    status,
    headers,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const readyBody = {
  state: 'ready',
  audioURL: '/api/v2/media/species-sound/corvus_brachyrhynchos',
  recordist: 'Jane Doe',
  licenseName: 'CC BY-NC 4.0',
};

function fetchReturning(result: Response | Error) {
  return vi.fn().mockImplementation(() => {
    if (result instanceof Error) return Promise.reject(result);
    return Promise.resolve(result);
  });
}

describe('speciesSoundInfoUrl', () => {
  it('addresses the info endpoint for one species', () => {
    expect(speciesSoundInfoUrl('Corvus brachyrhynchos')).toContain(
      '/api/v2/media/species-sound/Corvus%20brachyrhynchos/info'
    );
  });

  it('trims the name before encoding it', () => {
    expect(speciesSoundInfoUrl('  Turdus merula  ')).toContain(
      '/api/v2/media/species-sound/Turdus%20merula/info'
    );
  });
});

describe('parseRetryAfter', () => {
  it('takes a plain number of seconds', () => {
    expect(parseRetryAfter('7')).toBe(7);
  });

  it('falls back for missing, malformed, non-positive and absurd values', () => {
    for (const value of [null, '', 'abc', '0', '-3', '3600']) {
      expect(parseRetryAfter(value)).toBe(DEFAULT_RETRY_AFTER_SECONDS);
    }
  });
});

describe('loadSpeciesSound', () => {
  it('returns the metadata when a clip is cached', async () => {
    const fetchImpl = fetchReturning(response({ body: readyBody }));
    const result = await loadSpeciesSound('Corvus brachyrhynchos', fetchImpl);

    expect(result).toEqual({
      kind: 'ready',
      info: expect.objectContaining({ recordist: 'Jane Doe' }),
    });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it('treats a cached "no recording" answer as unavailable, not an error', async () => {
    const result = await loadSpeciesSound('Some bird', fetchReturning(response({ status: 404 })));
    expect(result.kind).toBe('unavailable');
  });

  it('treats a 503 with Retry-After as a fetch in progress', async () => {
    const result = await loadSpeciesSound(
      'Some bird',
      fetchReturning(response({ status: 503, retryAfter: '9' }))
    );
    expect(result).toEqual({ kind: 'pending', retryAfterSeconds: 9 });
  });

  it('treats a 503 without Retry-After as unavailable, since that is the feature being off', async () => {
    const result = await loadSpeciesSound('Some bird', fetchReturning(response({ status: 503 })));
    expect(result.kind).toBe('unavailable');
  });

  it('reports a success without an audio URL as an error rather than a playable clip', async () => {
    const result = await loadSpeciesSound(
      'Some bird',
      fetchReturning(response({ body: { state: 'ready' } }))
    );
    expect(result.kind).toBe('error');
  });

  it('reports a transport failure without throwing', async () => {
    const result = await loadSpeciesSound('Some bird', fetchReturning(new Error('offline')));
    expect(result.kind).toBe('error');
  });

  it('does not ask the network about an empty name', async () => {
    const fetchImpl = fetchReturning(response({ body: readyBody }));
    const result = await loadSpeciesSound('   ', fetchImpl);

    expect(result.kind).toBe('unavailable');
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

describe('describeSpeciesSoundAttribution', () => {
  const labels = {
    withLicense: (recordist: string, license: string) => `${recordist} / ${license}`,
    withoutLicense: (recordist: string) => recordist,
    unknown: () => 'via Xeno-Canto',
  };

  it('names the recordist and the licence', () => {
    expect(
      describeSpeciesSoundAttribution(
        { state: 'ready', recordist: 'Jane Doe', licenseName: 'CC BY 4.0' },
        labels
      )
    ).toBe('Jane Doe / CC BY 4.0');
  });

  it('leaves the licence out when it is not recorded', () => {
    expect(describeSpeciesSoundAttribution({ state: 'ready', recordist: 'Jane Doe' }, labels)).toBe(
      'Jane Doe'
    );
  });

  it('still credits the archive when the recordist is unknown', () => {
    expect(describeSpeciesSoundAttribution({ state: 'ready', recordist: '   ' }, labels)).toBe(
      'via Xeno-Canto'
    );
    expect(describeSpeciesSoundAttribution(null, labels)).toBe('via Xeno-Canto');
  });
});
