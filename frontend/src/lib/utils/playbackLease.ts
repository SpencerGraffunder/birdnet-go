/**
 * One clip at a time, app-wide.
 *
 * Every player in this app owns its own `HTMLAudioElement` (the detection rows use
 * `useAudioPlayback`, the detail/search players use `AudioPlayer`, the dashboard uses
 * `PlayOverlay` and `MiniSpectrogram`), and nothing connected them: start a second clip
 * and the first kept playing over it. Overlap is never what a listener wants here — the
 * point of these clips is hearing one bird — and the Web Audio `AudioContext` is a shared
 * singleton (`getAudioContext()` in `audioContextManager.ts`), so two processed clips also
 * fight over one gain/EQ graph: the quieter bird ends up distorted rather than merely
 * doubled.
 *
 * A player opts in once, at the moment its element exists:
 *
 * ```ts
 * const audio = new Audio();
 * const stopFollowing = followSinglePlayback(audio);
 * ```
 *
 * From then on, whenever that element starts playing, every other followed element that is
 * playing gets paused — which is exactly what the browser needs to hear: the pause fires
 * each other player's own `pause` handler, so its play button, scrubber and any clip-extraction
 * state fall back through the code that already handles a manual pause. Nothing here touches
 * UI state directly, so a player keeps its own teardown rules.
 *
 * The tracked set holds only elements that are CURRENTLY playing: an element joins on `play`
 * and leaves on `pause`/`ended` (or when it is detached), so a long-lived page does not
 * accumulate references to rows that were played hours ago and since removed from the DOM.
 *
 * Deliberately NOT followed: the live stream page (`LiveStreamPage.svelte` — a continuous
 * stream the user chose to listen to, not a recording; pausing it because a clip started
 * would be a different policy) and `AudioLevelIndicator` (an analysis-only element).
 */

/** Elements that are playing right now, across every player in the document. */
const playing = new Set<HTMLMediaElement>();

/**
 * Make `element` part of the one-clip-at-a-time rule. Returns a detach function; call it when
 * the element goes away. Pausing failures are ignored on purpose: a player whose element is
 * torn down mid-click must not keep the rest of the clips playing.
 */
export function followSinglePlayback(element: HTMLMediaElement): () => void {
  const onPlay = (): void => {
    playing.add(element);
    for (const other of playing) {
      if (other === element) continue;
      if (other.paused) {
        // Stale entry: whatever held it never reported a pause (detached mid-playback).
        playing.delete(other);
        continue;
      }
      try {
        other.pause();
      } catch {
        playing.delete(other);
      }
    }
  };
  const onStop = (): void => {
    playing.delete(element);
  };

  element.addEventListener('play', onPlay);
  element.addEventListener('pause', onStop);
  element.addEventListener('ended', onStop);

  return () => {
    element.removeEventListener('play', onPlay);
    element.removeEventListener('pause', onStop);
    element.removeEventListener('ended', onStop);
    playing.delete(element);
  };
}

/** Clips playing right now. Tests use this to assert the rule instead of trusting UI state. */
export function activeClipCount(): number {
  return playing.size;
}
