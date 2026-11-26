"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { Pause, Play } from "lucide-react";
import { useTranslations } from "next-intl";
import { type KeyboardEvent, type PointerEvent, useEffect, useRef, useState } from "react";

/** How many bars the waveform is drawn with, as in Telegram's own player. */
const BARS = 48;

/**
 * A voice message or audio file, played the way Telegram shows one: a round
 * play button, a waveform of the recording's own loudness that fills as it
 * plays, and the length and size underneath.
 *
 * The waveform is computed in the browser from the bytes already fetched for
 * playback, so nothing extra is stored or requested. A recording the browser
 * cannot decode still plays when it can, over a flat waveform; one it cannot
 * play at all reports so through `onUnplayable`.
 */
export function VoicePlayer({
  source,
  data,
  label,
  sizeLabel,
  onUnplayable,
}: {
  /** An object URL for the recording. */
  source: string;
  /** The recording's bytes, for the waveform. */
  data: ArrayBuffer;
  label: string;
  sizeLabel: string;
  onUnplayable: () => void;
}) {
  const translate = useTranslations("admin.support.attachments");
  const audio = useRef<HTMLAudioElement>(null);
  const [peaks, setPeaks] = useState<number[]>(() => Array(BARS).fill(0.25));
  const [playing, setPlaying] = useState(false);
  const [duration, setDuration] = useState(0);
  const [position, setPosition] = useState(0);

  useEffect(() => {
    let cancelled = false;
    waveform(data)
      .then((computed) => {
        if (!cancelled && computed) {
          setPeaks(computed);
        }
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [data]);

  // A frame loop rather than `timeupdate`, which fires only a few times a
  // second and makes the fill step instead of flow.
  useEffect(() => {
    if (!playing) {
      return;
    }
    let frame = 0;
    const tick = () => {
      setPosition(audio.current?.currentTime ?? 0);
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [playing]);

  const progress = duration > 0 ? Math.min(position / duration, 1) : 0;

  function toggle() {
    const element = audio.current;
    if (!element) {
      return;
    }
    if (element.paused) {
      element.play().catch(onUnplayable);
    } else {
      element.pause();
    }
  }

  function seekTo(fraction: number) {
    const element = audio.current;
    if (!element || !Number.isFinite(element.duration) || element.duration <= 0) {
      return;
    }
    element.currentTime = Math.min(Math.max(fraction, 0), 1) * element.duration;
    setPosition(element.currentTime);
  }

  function seekFromPointer(event: PointerEvent<HTMLDivElement>) {
    const box = event.currentTarget.getBoundingClientRect();
    seekTo((event.clientX - box.left) / box.width);
  }

  function seekFromKey(event: KeyboardEvent<HTMLDivElement>) {
    const element = audio.current;
    if (!element || duration <= 0) {
      return;
    }
    const step = { ArrowLeft: -5, ArrowRight: 5, Home: -Infinity, End: Infinity }[event.key];
    if (step === undefined) {
      return;
    }
    event.preventDefault();
    seekTo((element.currentTime + step) / duration);
  }

  return (
    <div className="flex w-80 max-w-full items-center gap-3 py-1">
      <audio
        onDurationChange={(event) => {
          const value = event.currentTarget.duration;
          if (Number.isFinite(value)) {
            setDuration(value);
          }
        }}
        onEnded={() => {
          setPlaying(false);
          setPosition(0);
        }}
        onError={onUnplayable}
        onLoadedMetadata={(event) => {
          const value = event.currentTarget.duration;
          if (Number.isFinite(value)) {
            setDuration(value);
          }
        }}
        onPause={() => setPlaying(false)}
        onPlay={() => setPlaying(true)}
        preload="metadata"
        ref={audio}
        src={source}
      >
        <track kind="captions" />
      </audio>
      <button
        aria-label={playing ? translate("pause") : translate("play")}
        className="flex size-10 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground transition-transform hover:scale-105 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 active:scale-95"
        onClick={toggle}
        type="button"
      >
        {playing ? (
          <Pause aria-hidden className="size-4 fill-current" />
        ) : (
          <Play aria-hidden className="ml-0.5 size-4 fill-current" />
        )}
      </button>
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div
          aria-label={translate("seek", { name: label })}
          aria-valuemax={Math.round(duration)}
          aria-valuemin={0}
          aria-valuenow={Math.round(position)}
          aria-valuetext={`${clock(position)} / ${clock(duration)}`}
          className="flex h-6 cursor-pointer items-center gap-[2px] rounded-sm focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
          onKeyDown={seekFromKey}
          onPointerDown={(event) => {
            event.currentTarget.setPointerCapture(event.pointerId);
            seekFromPointer(event);
          }}
          onPointerMove={(event) => {
            if (event.currentTarget.hasPointerCapture(event.pointerId)) {
              seekFromPointer(event);
            }
          }}
          role="slider"
          tabIndex={0}
        >
          {peaks.map((peak, index) => (
            <span
              className={cn(
                "w-[3px] shrink-0 rounded-full transition-colors",
                (index + 0.5) / peaks.length <= progress ? "bg-primary" : "bg-primary/35",
              )}
              // biome-ignore lint/suspicious/noArrayIndexKey: bars are positional and never reorder.
              key={index}
              style={{ height: `${Math.max(12, Math.round(peak * 100))}%` }}
            />
          ))}
        </div>
        <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
          {clock(playing || position > 0 ? position : duration)}, {sizeLabel}
        </span>
      </div>
    </div>
  );
}

/** `m:ss`, or `h:mm:ss` past an hour. */
function clock(seconds: number): string {
  const total = Number.isFinite(seconds) && seconds > 0 ? Math.floor(seconds) : 0;
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const rest = String(total % 60).padStart(2, "0");
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${rest}` : `${minutes}:${rest}`;
}

/**
 * The recording's loudness in `BARS` buckets, 0 to 1, from the peak of each
 * bucket. Null when the browser cannot decode the format.
 */
async function waveform(data: ArrayBuffer): Promise<number[] | null> {
  const Context =
    typeof window === "undefined"
      ? undefined
      : (window.AudioContext ??
        (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext);
  if (!Context) {
    return null;
  }
  const context = new Context();
  try {
    // decodeAudioData detaches the buffer it is given; the copy keeps the
    // caller's bytes intact.
    const decoded = await context.decodeAudioData(data.slice(0));
    const samples = decoded.getChannelData(0);
    const bucket = Math.max(1, Math.floor(samples.length / BARS));
    const peaks: number[] = [];
    for (let bar = 0; bar < BARS; bar++) {
      let peak = 0;
      const end = Math.min(samples.length, (bar + 1) * bucket);
      for (let index = bar * bucket; index < end; index++) {
        const value = Math.abs(samples[index] ?? 0);
        if (value > peak) {
          peak = value;
        }
      }
      peaks.push(peak);
    }
    const loudest = Math.max(...peaks, 0.0001);
    return peaks.map((peak) => peak / loudest);
  } catch {
    return null;
  } finally {
    void context.close();
  }
}
