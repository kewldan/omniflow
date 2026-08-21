"use client";

import { ApiError, type ProblemDetail } from "@/lib/api";

/**
 * Fetches a file through the API and hands it to the browser as a download.
 *
 * A plain `<a download>` would carry the session cookie just as well, but it
 * has no error path: a 409 "this file lives in Telegram" or a 404 "no longer
 * stored" would be saved to disk as a JSON file named like the attachment, and
 * the operator would open it expecting a screenshot. Going through `fetch`
 * lets a problem response become an `ApiError` the screen can explain, and only
 * a real body becomes a download.
 *
 * The object URL is revoked after the click has been dispatched; revoking it
 * synchronously races the browser's own read of the blob in some engines.
 */
export async function downloadThroughApi(path: string, fileName: string): Promise<void> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) {
    let problem: ProblemDetail | null = null;
    try {
      problem = (await response.json()) as ProblemDetail;
    } catch {
      problem = null;
    }
    throw new ApiError(response.status, problem);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
