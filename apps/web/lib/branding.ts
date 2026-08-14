import "server-only";

/**
 * What an installation looks like, read on the server before anything paints.
 *
 * This is fetched in the root layout rather than in a client component on
 * purpose. A palette applied after hydration is a visible flash of somebody
 * else's brand on every cold load, and the page most likely to be somebody's
 * first — a sign-in screen — is exactly the one that would flash.
 *
 * It is also why the stylesheet arrives as a finished string rather than as a
 * palette this code assembles. The API validates every value against an
 * allowlist of token names and a hex-only colour parser, and renders the CSS
 * itself; nothing an operator typed is ever concatenated here.
 */

export type Branding = {
  serviceName: string;
  /** Ready-to-inline declarations, or "" when nothing has been customised. */
  css: string;
  radius: string;
  density: string;
  /** Which modes a visitor may switch between. */
  allowedThemes: string[];
  /** "light", "dark", or "system". */
  defaultTheme: string;
  /** Slot name to a URL carrying the image's own checksum. */
  assets: Partial<Record<"logo_light" | "logo_dark" | "favicon", string>>;
};

const API_BASE = process.env.API_INTERNAL_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "";

/**
 * The design as shipped: two modes, following the operating system, nothing
 * overridden. It is what an installation gets when it has customised nothing,
 * when its API is unreachable, and when it runs no operator panel at all — the
 * last of which is a real configuration rather than a failure.
 */
const SHIPPED: Branding = {
  serviceName: "",
  css: "",
  radius: "default",
  density: "default",
  allowedThemes: ["light", "dark"],
  defaultTheme: "system",
  assets: {},
};

export async function readBranding(): Promise<Branding> {
  if (!API_BASE) {
    return SHIPPED;
  }
  try {
    const response = await fetch(`${API_BASE}/v1/branding`, {
      // A minute matches what the API asks for. Every page in both panels goes
      // through this, so re-reading it per request would put a database round
      // trip in front of every render for a value that changes when somebody
      // presses save.
      next: { revalidate: 60 },
    });
    if (!response.ok) {
      return SHIPPED;
    }
    const payload = (await response.json()) as Partial<Branding>;
    return {
      ...SHIPPED,
      ...payload,
      // A published theme that offers no mode at all would leave the toggle
      // with nothing to choose, so the shipped pair stands in.
      allowedThemes: payload.allowedThemes?.length ? payload.allowedThemes : SHIPPED.allowedThemes,
      assets: payload.assets ?? {},
    };
  } catch {
    // An unreachable API must not stop a page rendering. The panels already
    // surface that failure where it matters — on the data they cannot load —
    // and a blank screen because a colour could not be read would be worse
    // than the wrong colour.
    return SHIPPED;
  }
}
