import "server-only";

import type { Metadata } from "next";
import { getTranslations } from "next-intl/server";

import { readBranding } from "./branding";

/** What the installation calls itself, falling back to the product's name. */
export async function brandName(): Promise<string> {
  const branding = await readBranding();
  return branding.serviceName.trim() || "Omniflow";
}

/**
 * The tab title for one area of the application.
 *
 * A nested layout's `title.template` replaces the root's rather than wrapping
 * it, so an area that wants its own label must repeat the installation's name
 * itself. Hard-coding "Omniflow" there is what put somebody else's brand in the
 * tab of an operator selling under their own.
 *
 * `area` is a key under `common.meta`; `separateArea` says whether the area's
 * label also follows each page's own title (the operator panel, where "Admin"
 * distinguishes a tab from the customer panel of the same installation).
 */
export async function areaTitle(
  area: "account" | "admin" | "signIn",
  separateArea = false,
): Promise<Metadata["title"]> {
  const [name, translate] = await Promise.all([brandName(), getTranslations("common.meta")]);
  const label = translate(area);
  return {
    default: `${label} · ${name}`,
    template: separateArea ? `%s · ${label} · ${name}` : `%s · ${name}`,
  };
}
