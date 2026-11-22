"use client";

import { useEffect } from "react";

/**
 * Keeps `customer-ui` on <html> while a customer surface is mounted.
 *
 * The root layout sets it on the server for the first paint; this covers a
 * client-side navigation into or out of the customer panel, after which the
 * root layout is not rendered again.
 */
export function SurfaceClass() {
  useEffect(() => {
    const root = document.documentElement;
    root.classList.add("customer-ui");
    return () => root.classList.remove("customer-ui");
  }, []);
  return null;
}
