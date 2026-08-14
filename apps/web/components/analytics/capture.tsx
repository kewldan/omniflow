"use client";

import { usePathname, useSearchParams } from "next/navigation";
import { useEffect } from "react";

import { captureAttribution, readMeasurementChoice } from "@/lib/attribution";

/**
 * Reads an advertisement's parameters out of the landing URL and holds them.
 *
 * It runs only where a purchase can begin, and only after the visitor has
 * agreed to measurement. Both conditions matter: capturing on a page nobody
 * buys from stores something that will never be attached to anything, and
 * capturing before consent would mean a visitor who then declines had already
 * been marked.
 *
 * The capture is first-write-wins over a thirty-day window. Somebody who lands
 * from an advertisement, thinks about it for a week, and returns through a link
 * a friend sent was brought here by the advertisement — overwriting on the
 * second visit would credit the friend.
 *
 * Nothing is sent anywhere by this. The parameters sit first-party until a
 * purchase exists to attach them to, and if none ever does they age out.
 */
export function AttributionCapture() {
  const search = useSearchParams();
  const pathname = usePathname();

  useEffect(() => {
    // The operator panel is out of scope entirely. Nobody advertises their way
    // to an admin login, and an operator is not a person to be measured.
    if (pathname.startsWith("/admin")) {
      return;
    }
    if (readMeasurementChoice() !== "granted") {
      return;
    }
    captureAttribution(search.toString());
  }, [pathname, search]);

  return null;
}
