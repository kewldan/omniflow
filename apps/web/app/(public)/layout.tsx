import type { ReactNode } from "react";

import { SurfaceClass } from "@/components/surface-class";

/**
 * The public pages — documents, guest checkout, status, unsubscribe — are
 * customer surfaces. The root layout marks them so on a first load; this
 * keeps the mark when a customer follows a link here from the cabinet, which
 * unmounts the customer layout and would otherwise leave these pages in the
 * operator panel's colours.
 */
export default function PublicGroupLayout({ children }: { children: ReactNode }) {
  return (
    <>
      <SurfaceClass />
      {children}
    </>
  );
}
