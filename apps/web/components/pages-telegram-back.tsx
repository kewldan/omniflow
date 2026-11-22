"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";

import { haptic, setTelegramBackButton, startTelegram } from "@/lib/tma";

/**
 * Telegram's back button on the published documents.
 *
 * The documents sit outside the account shell, so the shell's handler is gone
 * by the time one opens, while Telegram still shows the button it set: a press
 * did nothing. Here a document goes back to the list, and the list goes back
 * to the account screen that links to it. Outside Telegram it does nothing.
 */
export function PagesTelegramBack() {
  const pathname = usePathname();
  const router = useRouter();
  useEffect(() => {
    if (!startTelegram()) {
      return;
    }
    const target = pathname === "/pages" ? "/account/profile" : "/pages";
    return setTelegramBackButton(() => {
      haptic.tap();
      router.push(target);
    });
  }, [pathname, router]);
  return null;
}
