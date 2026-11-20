import type { MetadataRoute } from "next";

import { readBranding } from "@/lib/branding";

/**
 * The web app manifest, so the customer panel installs to a phone's home
 * screen as an app — the way Bedolaga's cabinet is commonly used — under the
 * installation's own name and favicon. It starts at the customer panel,
 * which is the surface a customer installs; it is generated per request from
 * the branding rather than shipped as a file, so a rename applies at once.
 */
export default async function manifest(): Promise<MetadataRoute.Manifest> {
  const branding = await readBranding();
  const name = branding.serviceName.trim() || "Omniflow";
  const icons: MetadataRoute.Manifest["icons"] = branding.assets.favicon
    ? [{ src: branding.assets.favicon, sizes: "any" }]
    : [];
  return {
    name,
    short_name: name.slice(0, 12),
    start_url: "/account",
    scope: "/",
    display: "standalone",
    background_color: "#0b0b0c",
    theme_color: "#0b0b0c",
    icons,
  };
}
