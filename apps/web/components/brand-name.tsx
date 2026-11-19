"use client";

import { createContext, type ReactNode, useContext } from "react";

/**
 * What the installation calls itself, for client components.
 *
 * The customer panel's header, sidebar, and sign-in screen printed the
 * catalogue's placeholder ("VPN") whatever the operator entered under Brand →
 * Service name, because the name is read on the server and nothing handed it
 * to the browser. The root layout provides it here; an empty string means the
 * operator has not named the service and the caller's own fallback applies.
 */
const BrandNameContext = createContext("");

export function BrandNameProvider({ children, name }: { children: ReactNode; name: string }) {
  return <BrandNameContext value={name.trim()}>{children}</BrandNameContext>;
}

/** The service name, or `fallback` when the operator has not set one. */
export function useBrandName(fallback: string): string {
  return useContext(BrandNameContext) || fallback;
}
