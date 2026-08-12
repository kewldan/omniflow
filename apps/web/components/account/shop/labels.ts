"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

/**
 * What one unit of a product actually is, in words.
 *
 * A shop row reads "Telegram Premium" everywhere, so the useful part of the
 * name is the measure beside it: three months, or five hundred Stars. Both come
 * from a plural message rather than a concatenated number, because Russian
 * needs three forms where English needs two and building the phrase in code
 * would produce "3 месяц" for half the catalogue.
 */
export function useGoodsMeasure(): (goods: {
  kind: string;
  durationMonths?: number;
  starQuantity?: number;
}) => string {
  const translate = useTranslations("account.shop");
  return useCallback(
    (goods) => {
      if (goods.kind === "telegram_premium" && goods.durationMonths) {
        return translate("measure.months", { count: goods.durationMonths });
      }
      if (goods.kind === "telegram_stars" && goods.starQuantity) {
        return translate("measure.stars", { count: goods.starQuantity });
      }
      return "";
    },
    [translate],
  );
}
