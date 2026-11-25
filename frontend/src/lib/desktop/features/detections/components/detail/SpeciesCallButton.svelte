<!--
  SpeciesCallButton - plays a short published example call of the species so a
  detection can be compared with the real thing.

  The button's own label never changes between species - only between play and
  pause - because the detection card keeps one height for every detection. Why a
  call cannot be played goes in the short text beside it, which is what the
  button points at with aria-describedby while it is blocked, following the same
  arrangement the review controls use. Attribution belongs to the recording, so
  it is shown here rather than under the species photo, which is credited
  separately, and it stays in the tooltip so it does not take room in the layout.
-->
<script lang="ts">
  import { Pause, Play } from '@lucide/svelte';
  import { onDestroy, untrack } from 'svelte';
  import Button from '$lib/desktop/components/ui/Button.svelte';
  import { t } from '$lib/i18n';
  import { buildAppUrl } from '$lib/utils/urlHelpers';
  import { generateId } from '$lib/utils/uuid';
  import {
    describeSpeciesSoundAttribution,
    loadSpeciesSound,
    type SpeciesSoundInfo,
  } from './speciesCall';

  interface Props {
    scientificName: string;
  }

  let { scientificName }: Props = $props();

  /** What is known about this species' example call. */
  type Phase = 'loading' | 'ready' | 'unavailable' | 'error';

  // A first sighting starts a fetch on Xeno-Canto's side, so "not yet" is a
  // normal answer. A few spaced retries cover the seconds it takes without
  // leaving a request loop running on a species that will never have a clip.
  const MAX_PENDING_RETRIES = 3;

  const statusId = generateId('species-call');

  let phase = $state<Phase>('loading');
  let playing = $state(false);
  let info = $state<SpeciesSoundInfo | null>(null);
  let audio = $state<HTMLAudioElement | null>(null);
  let pendingRetries = 0;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  /** Bumped when the species changes so a slow answer about the previous bird
      cannot overwrite the state of the one now on screen. */
  let generation = 0;

  function clearRetry() {
    if (retryTimer !== undefined) {
      clearTimeout(retryTimer);
      retryTimer = undefined;
    }
  }

  function stopPlayback() {
    playing = false;
    // Asking a player with nothing loaded to pause is a no-op in a browser and
    // noise in a test runner, so it is only done when something is playing.
    if (audio && !audio.paused) {
      audio.pause();
    }
  }

  async function lookup(name: string) {
    const wanted = generation;
    const result = await loadSpeciesSound(name);
    if (wanted !== generation) return;

    switch (result.kind) {
      case 'ready':
        info = result.info;
        phase = 'ready';
        return;
      case 'pending':
        if (pendingRetries >= MAX_PENDING_RETRIES) {
          phase = 'unavailable';
          return;
        }
        pendingRetries += 1;
        clearRetry();
        retryTimer = setTimeout(() => {
          void lookup(name);
        }, result.retryAfterSeconds * 1000);
        return;
      case 'unavailable':
      case 'error':
        phase = result.kind;
        return;
    }
  }

  // One lookup per species. The detail panel keeps this component mounted while
  // stepping through detections, so the bird changes under it.
  //
  // Only the name may be tracked here: the reset and the playback stop touch the
  // bound <audio> element, and letting that count as a dependency would make
  // every successful lookup re-run itself.
  $effect(() => {
    const name = scientificName;
    untrack(() => {
      generation += 1;
      clearRetry();
      stopPlayback();
      info = null;
      phase = 'loading';
      pendingRetries = 0;
      void lookup(name);
    });
    return () => clearRetry();
  });

  // Playback has to stop when the detection changes or the panel closes, or a
  // clip keeps playing over a different bird.
  onDestroy(() => {
    clearRetry();
    stopPlayback();
  });

  function togglePlayback() {
    // aria-disabled leaves the button clickable on purpose, so the reason for a
    // blocked call stays reachable; the action itself is still ignored.
    if (phase !== 'ready' || !info?.audioURL || !audio) return;
    if (playing) {
      audio.pause();
      return;
    }
    // A refused play (autoplay policy, unsupported codec) must not leave the
    // button showing that something is playing.
    audio.play().catch(() => {
      playing = false;
    });
  }

  const statusText = $derived.by(() => {
    switch (phase) {
      case 'loading':
        return t('detections.detail.identity.callLoading');
      case 'unavailable':
        return t('detections.detail.identity.callUnavailable');
      case 'error':
        return t('detections.detail.identity.callError');
      case 'ready':
        return '';
    }
  });

  const attribution = $derived(
    describeSpeciesSoundAttribution(info, {
      withLicense: (recordist, license) =>
        t('detections.detail.identity.callCredit', { recordist, license }),
      withoutLicense: recordist =>
        t('detections.detail.identity.callCreditNoLicense', { recordist }),
      unknown: () => t('detections.detail.identity.callCreditUnknown'),
    })
  );

  const tooltip = $derived.by(() => {
    if (phase !== 'ready') return statusText;
    const trimmedNote =
      info?.trimmed && info.originalSeconds
        ? t('detections.detail.identity.callTrimmed', {
            length: Math.round(info.originalSeconds),
          })
        : '';
    return trimmedNote ? `${attribution} · ${trimmedNote}` : attribution;
  });

  const label = $derived(
    playing ? t('detections.detail.identity.pauseCall') : t('detections.detail.identity.playCall')
  );

  const audioSrc = $derived(phase === 'ready' && info?.audioURL ? buildAppUrl(info.audioURL) : '');

  const blocked = $derived(phase !== 'ready');
</script>

<div class="dd-call">
  <Button
    variant="ghost"
    size="xs"
    className="dd-call-button shrink-0 px-1.5 opacity-80 hover:opacity-100"
    aria-disabled={blocked ? 'true' : undefined}
    aria-describedby={blocked ? statusId : undefined}
    aria-pressed={blocked ? undefined : playing ? 'true' : 'false'}
    title={tooltip}
    onclick={togglePlayback}
  >
    {#if playing}
      <Pause size={14} aria-hidden="true" />
    {:else}
      <Play size={14} aria-hidden="true" />
    {/if}
    <span class="dd-call-label">{label}</span>
  </Button>

  {#if statusText}
    <!-- Not a live region: the reason is read through aria-describedby on the
         button, the way the review controls report a blocked verdict. -->
    <p class="dd-call-status" id={statusId} title={statusText}>{statusText}</p>
  {/if}

  {#if audioSrc}
    <audio
      bind:this={audio}
      src={audioSrc}
      preload="none"
      onplay={() => (playing = true)}
      onpause={() => (playing = false)}
      onended={() => (playing = false)}
      onerror={() => {
        playing = false;
        phase = 'error';
      }}
    ></audio>
  {/if}
</div>

<style>
  .dd-call {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    min-width: 0;

    /* One fixed line: a control whose height depends on whether a species has a
       call would change the height of the card around it. */
    min-height: 1.5rem;
    margin-top: 0.3rem;
  }

  .dd-call-label {
    white-space: nowrap;
  }

  .dd-call-status {
    min-width: 0;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.625rem;
    line-height: 1rem;
    color: var(--text-subtle);
  }
</style>
