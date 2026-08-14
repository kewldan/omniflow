"use client";

import { useEffect, useState } from "react";

/**
 * A QR rendered in the browser, never fetched as an image.
 *
 * Every value this component is asked to encode is a credential: a
 * subscription link, or the `otpauth://` URI that carries a TOTP secret.
 * Fetching a QR from an image endpoint — a third-party generator, or even this
 * application's own `/_next/image` — would put that credential into a request
 * line, a proxy log, and a cache. So the encoder runs here and the result never
 * leaves the tab.
 *
 * It is imported lazily so the encoder is absent from the bundle of every
 * screen that does not draw one.
 */
export function QrCode({
  alt,
  className,
  size = 320,
  value,
}: {
  alt: string;
  className?: string;
  /** Pixel width handed to the encoder; the element itself scales with its box. */
  size?: number;
  value: string;
}) {
  const [dataUrl, setDataUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setFailed(false);
    import("qrcode")
      .then((module) =>
        module.toDataURL(value, { errorCorrectionLevel: "M", margin: 1, width: size }),
      )
      .then((url) => {
        if (!cancelled) {
          setDataUrl(url);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setFailed(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [size, value]);

  // A failed encode renders nothing rather than a broken frame. Every caller
  // shows the same value as text beside this, so nobody is left without it.
  if (failed) {
    return null;
  }

  return dataUrl ? (
    // A plain <img>, not next/image: the source is a data URL produced in the
    // browser a moment ago, so there is nothing for the image pipeline to
    // optimise, and routing it through /_next/image would send a
    // credential-bearing payload to the server to be cached.
    // biome-ignore lint/performance/noImgElement: runtime-generated data URL, must not reach the image pipeline
    <img alt={alt} className={className} src={dataUrl} />
  ) : (
    <span aria-hidden className={className ?? "size-full animate-pulse bg-muted"} />
  );
}
