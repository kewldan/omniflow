"use client";

import { lazy, Suspense } from "react";

import type { PublicAnalytics } from "@/lib/analytics";

/**
 * The gate in front of the operator's advertising measurement.
 *
 * It is deliberately almost empty, and it uses React's own `lazy` rather than
 * `next/dynamic`. This component is rendered on every public and customer
 * route (never an operator one; see `customer-measurement.tsx`), so anything
 * reachable from here is paid for by the sign-in screen and the checkout —
 * whether or not the installation measures anything at all. `next/dynamic` brings a loader of
 * its own along with it; `lazy` and `Suspense` are already in the React runtime
 * the page has loaded regardless.
 *
 * `measurable` is false on an installation that has configured no counter,
 * which is the default and, for most installations, permanent. Then nothing
 * loads at all: no counter script, no consent banner, no click capture.
 */
const MeasurementRuntime = lazy(() =>
  import("./measurement-runtime").then((module) => ({ default: module.MeasurementRuntime })),
);

export function Measurement({ analytics, nonce }: { analytics: PublicAnalytics; nonce?: string }) {
  if (!analytics.measurable) {
    return null;
  }
  return (
    <Suspense fallback={null}>
      <MeasurementRuntime analytics={analytics} nonce={nonce} />
    </Suspense>
  );
}
