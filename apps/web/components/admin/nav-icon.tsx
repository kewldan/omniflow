"use client";

import type { AnimationItem, LottiePlayer } from "lottie-web";
import type { LucideIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";

/**
 * A sidebar icon that plays its animation when the entry is hovered.
 *
 * The animations are dotLottie files under /lottie/sidebar, one per entry,
 * drawn so their first frame is the static icon. The page renders the plain
 * icon first; the player and the files load once the browser is idle, then
 * replace it on that first frame, so nothing moves or flashes. Strokes are
 * drawn black and recoloured to the text colour in CSS (see globals.css), and
 * someone who asked for reduced motion keeps the static icon.
 */

let player: Promise<LottiePlayer> | null = null;

/** The light SVG player: no expressions, so no eval under the panel's CSP. */
function loadPlayer(): Promise<LottiePlayer> {
  player ??= import("lottie-web/build/player/lottie_light").then(
    (module) => module.default as unknown as LottiePlayer,
  );
  return player;
}

const animations = new Map<string, Promise<unknown>>();

/** Reads the one animation a .lottie carries, as its manifest names it. */
function loadAnimation(key: string): Promise<unknown> {
  let pending = animations.get(key);
  if (!pending) {
    pending = fetch(`/lottie/${key}.lottie`)
      .then((response) => {
        if (!response.ok) {
          throw new Error(`animation ${key}: ${response.status}`);
        }
        return response.arrayBuffer();
      })
      // The unzipper loads with the first animation, like the player, so it
      // costs nothing on a page until an icon is about to move.
      .then(async (buffer) => {
        const { strFromU8, unzipSync } = await import("fflate");
        const files = unzipSync(new Uint8Array(buffer));
        const manifest = JSON.parse(strFromU8(files["manifest.json"] ?? new Uint8Array())) as {
          animations?: { id: string }[];
        };
        const id = manifest.animations?.[0]?.id ?? key;
        const data = files[`animations/${id}.json`];
        if (!data) {
          throw new Error(`animation ${key}: no animations/${id}.json`);
        }
        return JSON.parse(strFromU8(data)) as unknown;
      });
    animations.set(key, pending);
  }
  return pending;
}

function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}

export function NavIcon({
  animation,
  className = "size-4",
  icon: Icon,
  play,
  set = "sidebar",
}: {
  /** The .lottie under /lottie/<set>; absent keeps the static icon. */
  animation?: string;
  /** The icon's size. */
  className?: string;
  icon: LucideIcon;
  /** Bumped on every hover or tap; each change plays the animation once. */
  play: number;
  /** Which set of animations: the operator sidebar's or the customer panel's. */
  set?: "sidebar" | "account";
}) {
  const box = useRef<HTMLSpanElement>(null);
  const item = useRef<AnimationItem | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    if (!animation || prefersReducedMotion()) {
      return;
    }
    let cancelled = false;
    let loaded: AnimationItem | null = null;
    const start = () => {
      Promise.all([loadPlayer(), loadAnimation(`${set}/${animation}`)])
        .then(([lottie, data]) => {
          if (cancelled || !box.current) {
            return;
          }
          loaded = lottie.loadAnimation({
            animationData: data,
            autoplay: false,
            container: box.current,
            loop: false,
            renderer: "svg",
          });
          loaded.goToAndStop(0, true);
          item.current = loaded;
          setReady(true);
        })
        // A file that does not load leaves the static icon, which is a
        // complete sidebar.
        .catch(() => undefined);
    };
    // Safari has no requestIdleCallback; a short timeout stands in for it.
    const idle = typeof window.requestIdleCallback === "function";
    const handle = idle
      ? window.requestIdleCallback(start, { timeout: 2000 })
      : globalThis.setTimeout(start, 300);
    return () => {
      cancelled = true;
      if (idle) {
        window.cancelIdleCallback(handle as number);
      } else {
        globalThis.clearTimeout(handle);
      }
      loaded?.destroy();
      item.current = null;
    };
  }, [animation, set]);

  useEffect(() => {
    if (play > 0) {
      item.current?.goToAndPlay(0, true);
    }
  }, [play]);

  return (
    <span aria-hidden="true" className={`nav-lottie relative shrink-0 ${className}`}>
      {!ready && <Icon className={className} />}
      <span className={ready ? "absolute inset-0" : "invisible absolute inset-0"} ref={box} />
    </span>
  );
}
