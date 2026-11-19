import type { ReactNode } from "react";

import { CornerLocaleSwitch } from "@/components/intl/corner-locale-switch";

/** The sign-in screens, with a way to change language before signing in. */
export default function CustomerSignInLayout({ children }: { children: ReactNode }) {
  return (
    <>
      <CornerLocaleSwitch />
      {children}
    </>
  );
}
