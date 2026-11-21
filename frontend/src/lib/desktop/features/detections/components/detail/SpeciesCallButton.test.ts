import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import SpeciesCallButton from './SpeciesCallButton.svelte';

// The test i18n mock returns the key for anything it has no translation for,
// which makes the expected wording exact rather than locale-dependent.
const PLAY = 'detections.detail.identity.playCall';
const PAUSE = 'detections.detail.identity.pauseCall';
const LOADING = 'detections.detail.identity.callLoading';
const UNAVAILABLE = 'detections.detail.identity.callUnavailable';
const ERROR = 'detections.detail.identity.callError';

const playMock = vi.fn<() => Promise<void>>();
const pauseMock = vi.fn<() => void>();

function response(status: number, body: unknown = {}, retryAfter?: string): Response {
  const headers = new Headers();
  if (retryAfter !== undefined) headers.set('Retry-After', retryAfter);
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
  trimmed: true,
  originalSeconds: 41.6,
};

function buttonOf(container: HTMLElement): HTMLButtonElement {
  const button = container.querySelector('.dd-call-button');
  if (!(button instanceof HTMLButtonElement)) throw new Error('play button is missing');
  return button;
}

function audioOf(container: HTMLElement): HTMLAudioElement | null {
  return container.querySelector('audio');
}

function statusOf(container: HTMLElement): HTMLElement | null {
  return container.querySelector('.dd-call-status');
}

describe('SpeciesCallButton', () => {
  beforeEach(() => {
    playMock.mockReset().mockResolvedValue(undefined);
    pauseMock.mockReset();
    window.HTMLMediaElement.prototype.play = playMock;
    window.HTMLMediaElement.prototype.pause = pauseMock;
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('is blocked and says so while the lookup is running', () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(() => new Promise(() => {}))
    );
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });

    const button = buttonOf(container);
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.textContent).toContain(PLAY);
    expect(statusOf(container)?.textContent).toBe(LOADING);
    // The blocked reason is reachable: describedby points at the element that says it.
    const describedBy = button.getAttribute('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(container.querySelector(`#${describedBy}`)).toBe(statusOf(container));
    expect(audioOf(container)).toBeNull();
  });

  it('ignores a click while blocked', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(503, {}, '5')));
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Turdus merula' },
    });

    await fireEvent.click(buttonOf(container));
    expect(playMock).not.toHaveBeenCalled();
  });

  it('plays the cached clip and follows the audio element for play and pause', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(200, readyBody)));
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });

    const button = buttonOf(container);
    await waitFor(() => expect(button.getAttribute('aria-disabled')).toBeNull());
    expect(button.getAttribute('aria-pressed')).toBe('false');
    expect(button.getAttribute('aria-describedby')).toBeNull();
    // The credit is a tooltip, not layout-stealing text, and it says the clip
    // was shortened from a longer recording. (The test i18n mock hands back the
    // key, so the keys are what identify the two parts.)
    const title = button.getAttribute('title') ?? '';
    expect(title).toContain('callCredit');
    expect(title).toContain('callTrimmed');

    const audio = audioOf(container);
    expect(audio?.getAttribute('src')).toContain(
      '/api/v2/media/species-sound/corvus_brachyrhynchos'
    );

    await fireEvent.click(button);
    expect(playMock).toHaveBeenCalledTimes(1);

    audio?.dispatchEvent(new Event('play'));
    await waitFor(() => expect(button.getAttribute('aria-pressed')).toBe('true'));
    expect(button.textContent).toContain(PAUSE);

    await fireEvent.click(button);
    expect(pauseMock).toHaveBeenCalledTimes(1);

    audio?.dispatchEvent(new Event('ended'));
    await waitFor(() => expect(button.getAttribute('aria-pressed')).toBe('false'));
    expect(button.textContent).toContain(PLAY);
  });

  it('says when the species has no published recording', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(404)));
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Some phantom bird' },
    });

    await waitFor(() => expect(statusOf(container)?.textContent).toBe(UNAVAILABLE));
    const button = buttonOf(container);
    expect(button.getAttribute('aria-disabled')).toBe('true');
    // The label stays the action, so the card looks the same for every species.
    expect(button.textContent).toContain(PLAY);
    expect(audioOf(container)).toBeNull();
  });

  it('reports a clip that the browser refuses to play', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(200, readyBody)));
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });

    const button = buttonOf(container);
    await waitFor(() => expect(audioOf(container)).not.toBeNull());
    audioOf(container)?.dispatchEvent(new Event('error'));
    await waitFor(() => expect(statusOf(container)?.textContent).toBe(ERROR));
    expect(button.getAttribute('aria-disabled')).toBe('true');
  });

  it('asks again after the wait the backend asked for, then plays', async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(503, {}, '5'))
      .mockResolvedValue(response(200, readyBody));
    vi.stubGlobal('fetch', fetchMock);
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(statusOf(container)?.textContent).toBe(LOADING);

    await vi.advanceTimersByTimeAsync(5000);
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(buttonOf(container).getAttribute('aria-disabled')).toBeNull();
    expect(audioOf(container)).not.toBeNull();
  });

  it('stops asking when a clip never arrives', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn().mockResolvedValue(response(503, {}, '5'));
    vi.stubGlobal('fetch', fetchMock);
    const { container } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });

    await vi.advanceTimersByTimeAsync(60000);
    // The first try plus the retries, and then it gives up.
    expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(4);
    expect(statusOf(container)?.textContent).toBe(UNAVAILABLE);
  });

  it('looks up the next species without letting a late answer about the previous one win', async () => {
    let lateResolve: ((value: Response) => void) | undefined;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.includes('Corvus')) {
        return new Promise<Response>(resolve => {
          lateResolve = resolve;
        });
      }
      return Promise.resolve(response(200, { ...readyBody, recordist: 'Late Bird Fan' }));
    });
    vi.stubGlobal('fetch', fetchMock);

    const { container, rerender } = render(SpeciesCallButton, {
      props: { scientificName: 'Corvus brachyrhynchos' },
    });
    await rerender({ scientificName: 'Turdus merula' });

    await waitFor(() => expect(buttonOf(container).getAttribute('aria-disabled')).toBeNull());

    // The answer about the bird that is no longer on screen arrives late.
    lateResolve?.(response(404));
    await new Promise(resolve => setTimeout(resolve, 0));

    expect(statusOf(container)).toBeNull();
    expect(buttonOf(container).getAttribute('title')).toContain('callCredit');
  });
});
