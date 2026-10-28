"use client";

import { StatusPage } from "@/components/status-page";

/**
 * A customer page that failed to render. It sits inside the account shell, so
 * the navigation stays and the customer can retry or go elsewhere.
 */
export default function AccountError({ reset }: { error: Error; reset: () => void }) {
  return <StatusPage kind="error" onRetry={reset} />;
}
