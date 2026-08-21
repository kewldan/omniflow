"use client";

import { apiFetch } from "@/lib/api";

import { idempotentHeaders } from "./idempotency";
import type { SupportAttachment, SupportLimits } from "./types";

/**
 * Posts one file to a conversation.
 *
 * It goes through the shared transport like every other request. A multipart
 * body needs the boundary the browser generates, and setting `Content-Type` at
 * all — to anything — destroys it; the transport therefore leaves the header
 * alone for a `FormData` body rather than each upload having to route around it.
 * Session cookie, CSRF token, token rotation, and `ApiError` all come for free.
 */
export async function uploadSupportAttachment(
  ticketId: string,
  file: File,
  message: string,
  idempotencyKey = "",
): Promise<SupportAttachment> {
  const body = new FormData();
  body.append("file", file);
  if (message.trim()) {
    body.append("message", message.trim());
  }

  return apiFetch<SupportAttachment>(
    `/v1/account/support/tickets/${encodeURIComponent(ticketId)}/attachments`,
    { body, headers: idempotentHeaders(idempotencyKey), method: "POST" },
  );
}

/**
 * The two refusals the browser can make before the network is touched.
 *
 * The server checks both again — it has to, since nothing in a browser is a
 * security boundary — but checking here first is what turns "your upload failed"
 * into "this file is 8 MB and the limit is 5 MB", stated before the customer
 * spends a minute sending it.
 */
export function rejectAttachment(
  file: File,
  limits: SupportLimits,
): { reason: "media_type" | "too_large" } | null {
  if (file.size > limits.maxAttachmentBytes) {
    return { reason: "too_large" };
  }
  // The type is compared without its parameters, exactly as the server
  // normalises it: `text/plain; charset=utf-8` is a text file, and a comparison
  // that said otherwise would be a rule about charsets.
  const mediaType = file.type.split(";")[0].trim().toLowerCase();
  const allowed = limits.allowedMediaTypes.some(
    (candidate) => candidate.trim().toLowerCase() === mediaType,
  );
  return allowed ? null : { reason: "media_type" };
}
