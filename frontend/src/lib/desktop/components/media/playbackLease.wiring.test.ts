/**
 * The one-clip-at-a-time rule, wired into the players.
 *
 * `playbackLease.test.ts` proves the rule itself. What these two tests prove is that the two
 * player implementations in this app actually join it: the detections list row (the
 * `useAudioPlayback` composable, reached through its test wrapper) and `AudioPlayer`, which is
 * the detail page's player, the search page's detail panel and the mobile sheet that wraps it.
 *
 * Each test plays a foreign clip first, then starts the player under test, and looks at the
 * foreign clip's `paused`. The fake media element cannot distinguish two `new Audio()` calls,
 * so the other side is always a hand-built element rather than a second player.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { cleanup, render } from '@testing-library/svelte';
import {
  installFakeMediaElement,
  type FakeMediaController,
} from '../../../../test/fakeMediaElement';
import { followSinglePlayback } from '$lib/utils/playbackLease';
import Wrapper from '../../../utils/UseAudioPlaybackWrapper.test.svelte';
import AudioPlayer from './AudioPlayer.svelte';

vi.mock('$lib/utils/audioContextManager', () => ({
  getAudioContext: vi.fn().mockResolvedValue({ state: 'running' }),
  isAudioContextSupported: vi.fn().mockReturnValue(true),
  releaseAudioContext: vi.fn(),
}));

vi.mock('$lib/utils/audioNodes', () => ({
  attachAudioGraphWhenRunning: vi.fn(),
  disconnectAudioNodes: vi.fn(),
}));

/** A clip that is part of the rule and is currently playing. */
function playingClip(): HTMLAudioElement {
  const element = document.createElement('audio');
  let paused = true;
  Object.defineProperty(element, 'paused', { configurable: true, get: () => paused });
  element.pause = (): void => {
    paused = true;
    element.dispatchEvent(new Event('pause'));
  };
  followSinglePlayback(element);
  // Join the way a real start does: the play event is what the rule reacts to.
  paused = false;
  element.dispatchEvent(new Event('play'));
  return element;
}

describe('one clip at a time', () => {
  let controller: FakeMediaController;

  beforeEach(() => {
    controller = installFakeMediaElement();
  });

  afterEach(() => {
    cleanup();
    controller.restore();
  });

  it('a detections row starting playback silences a clip already playing', () => {
    const other = playingClip();

    render(Wrapper, {
      props: { options: { audioUrl: '/api/v2/audio/1', detectionId: '1' } },
    });

    // The row's element starts playing (its own click path ends here in jsdom).
    controller.dispatch('play');

    expect(other.paused).toBe(true);
  });

  it('the detail/search player starting playback silences a clip already playing', () => {
    const other = playingClip();

    render(AudioPlayer, {
      props: { audioUrl: '/api/v2/audio/2', detectionId: '2', showSpectrogram: false },
    });

    controller.dispatch('play');

    expect(other.paused).toBe(true);
  });
});
