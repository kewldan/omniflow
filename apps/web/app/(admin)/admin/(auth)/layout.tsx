import type { Metadata } from "next";
import type { ReactNode } from "react";

import { areaTitle } from "@/lib/metadata";

export async function generateMetadata(): Promise<Metadata> {
  return {
    title: await areaTitle("signIn", true),
    robots: { index: false, follow: false },
  };
}

/**
 * Unauthenticated panel routes: sign-in, two-factor, and first-owner setup.
 *
 * These deliberately sit outside the panel shell. They cannot depend on a
 * session, and nesting them under the shell would send the operator into a
 * redirect loop the moment the session lookup returned 401.
 */
export default function AdminAuthLayout({ children }: { children: ReactNode }) {
  return children;
}
