"use client";

import { ApiError, type ProblemDetail } from "@/lib/api";

/**
 * Fetches an attachment through the API and hands it to the browser.
 *
 * A plain `<a download>` carries the session cookie just as well, but it has
 * no error path: a 409 "this file lives in Telegram", a 404 for a purged file,
 * or a 503 from the attachment store would be saved to disk as a JSON file
 * named like the screenshot, and the customer would open it expecting an
 * image. Going through `fetch` turns a problem response into an `ApiError`
 * the screen can explain, and only a real body becomes a download.
 *
 * The object URL is revoked after the click has been dispatched; revoking it
 * synchronously races the browser's own read of the blob in some engines.
 */
export async function downloadSupportAttachment(
  attachmentId: string,
  fileName: string,
): Promise<void> {
  const response = await fetch(
    `/v1/account/support/attachments/${encodeURIComponent(attachmentId)}`,
    { credentials: "include" },
  );
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
