/**
 * The one-clip-at-a-time rule.
 *
 * The interesting part of these tests is what a browser cannot do in jsdom: jsdom's
 * HTMLMediaElement has no playback engine, so `play()` fires nothing and `paused` is a
 * constant. Each fake clip here therefore owns its own `paused` flag and fires the events a
 * real element would, which is also what makes the assertions readable: play one clip, look
 * at the other.
 */

import { describe, expect, it, vi } from 'vitest';
import { activeClipCount, followSinglePlayback } from './playbackLease';

/** A clip whose `play()`/`pause()` behave like a real element's, events included. */
function fakeClip(name: string): HTMLAudioElement {
  const element = document.createElement('audio');
  let paused = true;
  element.dataset.clipName = name; // makes a failure message say which clip it is about
  Object.defineProperty(element, 'paused', {
    configurable: true,
    get: () => paused,
  });
  element.play = (): Promise<void> => {
    paused = false;
    element.dispatchEvent(new Event('play'));
    return Promise.resolve();
  };
  element.pause = (): void => {
    if (paused) return;
    paused = true;
    element.dataset.pauseHandled = 'yes';
    element.dispatchEvent(new Event('pause'));
  };
  return element;
}

/** Fire `ended` the way a real element does: playback stopped, position at the end. */
function endClip(element: HTMLAudioElement): void {
  element.dispatchEvent(new Event('ended'));
}

describe('followSinglePlayback', () => {
  it('pauses another playing clip when a second one starts', () => {
    const first = fakeClip('first');
    const second = fakeClip('second');
    followSinglePlayback(first);
    followSinglePlayback(second);

    void first.play();
    expect(first.paused).toBe(false);
    expect(activeClipCount()).toBe(1);

    void second.play();

    expect(second.paused).toBe(false);
    expect(first.paused).toBe(true);
    // The other player was paused through its own element, so its own `pause` handler —
    // the code path a manual pause takes — ran and its UI state follows.
    expect(first.dataset.pauseHandled).toBe('yes');
    expect(activeClipCount()).toBe(1);
  });

  it('hands the turn back when the first clip is replayed', () => {
    const first = fakeClip('first');
    const second = fakeClip('second');
    followSinglePlayback(first);
    followSinglePlayback(second);

    void first.play();
    void second.play();
    void first.play();

    expect(first.paused).toBe(false);
    expect(second.paused).toBe(true);
    expect(activeClipCount()).toBe(1);
  });

  it('holds no reference to a clip that paused or ended', () => {
    const clip = fakeClip('clip');
    const other = fakeClip('other');
    followSinglePlayback(clip);
    followSinglePlayback(other);

    void clip.play();
    clip.pause();
    expect(activeClipCount()).toBe(0);

    void other.play();
    endClip(other);
    // `ended` does not change `paused` on a real element, so the entry has to leave on the
    // event rather than by polling state.
    expect(activeClipCount()).toBe(0);
  });

  it('leaves a clip that never opted in alone', () => {
    // The live stream listens to a stream, not a recording: it is not followed, and a clip
    // starting must not cut it off.
    const unfollowed = fakeClip('live');
    const clip = fakeClip('clip');
    followSinglePlayback(clip);

    void unfollowed.play();
    void clip.play();

    expect(unfollowed.paused).toBe(false);
  });

  it('drops a clip whose pause throws instead of jamming the rule', () => {
    // A player torn down mid-playback can hand back an element whose pause() fails. The
    // rest of the clips still have to obey one-at-a-time.
    const broken = document.createElement('audio');
    Object.defineProperty(broken, 'paused', { configurable: true, get: () => false });
    broken.pause = () => {
      throw new Error('detached');
    };
    const clip = fakeClip('clip');
    followSinglePlayback(broken);
    followSinglePlayback(clip);

    // Join the set the way a real `play` event would.
    broken.dispatchEvent(new Event('play'));
    expect(activeClipCount()).toBe(1);

    const pauseError = vi.spyOn(console, 'error').mockImplementation(() => {});
    void clip.play();

    expect(broken.paused).toBe(false); // nothing else could stop it
    expect(activeClipCount()).toBe(1); // and it is no longer tracked (only `clip` remains)
    pauseError.mockRestore();
  });

  it('stops following a clip once detached', () => {
    const clip = fakeClip('clip');
    const other = fakeClip('other');
    const detach = followSinglePlayback(clip);
    followSinglePlayback(other);

    void clip.play();
    detach();

    void other.play();
    void clip.play();

    // `clip` is out of the rule: it no longer pauses anything, and nothing tracks it either.
    expect(clip.paused).toBe(false);
    expect(other.paused).toBe(false);
    expect(activeClipCount()).toBe(1);
  });
});
