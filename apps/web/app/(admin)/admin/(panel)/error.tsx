"use client";

import { StatusPage } from "@/components/status-page";

/**
 * A panel page that failed to render. It sits inside the panel layout, so the
 * sidebar and header stay and the operator can retry or go elsewhere.
 */
export default function PanelError({ reset }: { error: Error; reset: () => void }) {
  return <StatusPage kind="error" onRetry={reset} />;
}
