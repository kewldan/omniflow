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
  value,
}: {
  alt: string;
  className?: string;
  /** Kept for callers; the code is drawn as a vector and scales with its box. */
  size?: number;
  value: string;
}) {
  const [dataUrl, setDataUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setFailed(false);
    import("qrcode")
      .then((module) => {
        const code = module.create(value, { errorCorrectionLevel: "M" });
        return roundedQrDataUrl(code.modules.size, (x, y) => Boolean(code.modules.get(x, y)));
      })
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
  }, [value]);

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

/** The quiet zone around the code, in modules. */
const MARGIN = 2;

/**
 * Draws a QR code as an SVG with rounded corners: a white tile with rounded
 * corners, rounded finder eyes, and softly rounded modules. The modules sit
 * where the encoder put them, so the code scans exactly as a square one does.
 */
export function roundedQrSvg(size: number, dark: (x: number, y: number) => boolean): string {
  const total = size + MARGIN * 2;
  const finder = (x: number, y: number) =>
    (x < 7 && y < 7) || (x >= size - 7 && y < 7) || (x < 7 && y >= size - 7);
  const parts: string[] = [
    `<rect width="${total}" height="${total}" rx="${total * 0.06}" fill="#ffffff"/>`,
  ];
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      if (dark(x, y) && !finder(x, y)) {
        parts.push(
          `<rect x="${x + MARGIN + 0.05}" y="${y + MARGIN + 0.05}" width="0.9" height="0.9" rx="0.3"/>`,
        );
      }
    }
  }
  for (const [fx, fy] of [
    [0, 0],
    [size - 7, 0],
    [0, size - 7],
  ] as const) {
    const ox = fx + MARGIN;
    const oy = fy + MARGIN;
    parts.push(
      `<rect x="${ox + 0.5}" y="${oy + 0.5}" width="6" height="6" rx="1.6" fill="none" stroke="#000" stroke-width="1"/>`,
      `<rect x="${ox + 2}" y="${oy + 2}" width="3" height="3" rx="0.9"/>`,
    );
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="320" viewBox="0 0 ${total} ${total}" shape-rendering="geometricPrecision" fill="#000">${parts.join("")}</svg>`;
}

function roundedQrDataUrl(size: number, dark: (x: number, y: number) => boolean): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(roundedQrSvg(size, dark))}`;
}
