/**
 * The Content-Security-Policy for every HTML page, built per request by the proxy.
 *
 * Scripts are allowed by nonce with `'strict-dynamic'`: Next.js stamps the nonce
 * on its own scripts (it reads it from the request's policy header), and
 * anything those scripts load in turn — the Telegram widget, a counter's loader —
 * inherits the trust. Nothing else runs, which is the point: a customer's
 * browser holds subscription links and an operator's holds every customer's.
 *
 * Styles allow `'unsafe-inline'`. The operator's palette is an inline `<style>`
 * and the component library sets inline `style` attributes; neither can carry
 * script, and a nonce on every style attribute is not something React offers.
 *
 * The policy differs by surface, because the surfaces need different things:
 *
 * - **Operator pages** get the strict base and nothing else: no third-party
 *   origin at all, and no framing.
 * - **Public and customer pages** additionally allow the two counter providers
 *   an operator can configure (`internal/adtracking`), and may be framed by
 *   Telegram Web, which opens a Mini App in an iframe.
 * - **The customer sign-in screen** additionally allows the Telegram login
 *   widget's frame and requests — and `'unsafe-eval'`, because the widget
 *   compiles its `data-onauth` callback from a string with `eval`. That
 *   allowance is confined to the one screen that loads the widget.
 */

export type CspSurface = "admin" | "customer" | "sign-in";

/** The counter providers an operator can configure, by directive. */
const ANALYTICS = {
  script: [
    "https://mc.yandex.ru",
    "https://mc.yandex.com",
    "https://yastatic.net",
    "https://*.googletagmanager.com",
  ],
  connect: [
    "https://mc.yandex.ru",
    "https://mc.yandex.com",
    "https://*.google-analytics.com",
    "https://*.analytics.google.com",
    "https://*.googletagmanager.com",
  ],
  img: [
    "https://mc.yandex.ru",
    "https://mc.yandex.com",
    "https://*.google-analytics.com",
    "https://*.googletagmanager.com",
  ],
  frame: ["https://mc.yandex.ru", "https://mc.yandex.com"],
};

const TELEGRAM_WIDGET = {
  script: ["https://telegram.org"],
  frame: ["https://oauth.telegram.org"],
  connect: ["https://oauth.telegram.org"],
};

/** Which policy a path gets. */
export function cspSurface(pathname: string): CspSurface {
  if (pathname === "/admin" || pathname.startsWith("/admin/")) {
    return "admin";
  }
  if (pathname === "/account/sign-in" || pathname.startsWith("/account/sign-in/")) {
    return "sign-in";
  }
  return "customer";
}

/** A fresh, unguessable nonce. */
export function createNonce(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return btoa(String.fromCharCode(...bytes));
}

export function buildCsp(options: { nonce: string; surface: CspSurface; dev?: boolean }): string {
  const { nonce, surface, dev = false } = options;
  const customer = surface !== "admin";
  const signIn = surface === "sign-in";

  const script = ["'self'", `'nonce-${nonce}'`, "'strict-dynamic'"];
  const connect = ["'self'"];
  const img = ["'self'", "data:", "blob:"];
  const frame: string[] = [];

  if (customer) {
    // Host sources are ignored next to 'strict-dynamic' by every current
    // browser; they are the fallback for one that predates it.
    script.push(...ANALYTICS.script);
    connect.push(...ANALYTICS.connect);
    img.push(...ANALYTICS.img);
    frame.push(...ANALYTICS.frame);
  }
  if (signIn) {
    script.push(...TELEGRAM_WIDGET.script);
    connect.push(...TELEGRAM_WIDGET.connect);
    frame.push(...TELEGRAM_WIDGET.frame);
  }
  // React's development build evaluates code for its error overlay; the
  // widget evaluates its callback. Nothing else may.
  if (dev || signIn) {
    script.push("'unsafe-eval'");
  }

  const directives: Record<string, string[]> = {
    "default-src": ["'self'"],
    "script-src": script,
    "style-src": ["'self'", "'unsafe-inline'"],
    "img-src": img,
    "font-src": ["'self'", "data:"],
    "connect-src": connect,
    "frame-src": frame.length > 0 ? frame : ["'none'"],
    "worker-src": ["'self'", "blob:"],
    "object-src": ["'none'"],
    "base-uri": ["'self'"],
    "form-action": ["'self'"],
    // Telegram Web opens a Mini App in an iframe; nothing may frame the
    // operator panel.
    "frame-ancestors": customer ? ["'self'", "https://web.telegram.org"] : ["'none'"],
  };

  return Object.entries(directives)
    .map(([name, values]) => `${name} ${values.join(" ")}`)
    .join("; ");
}
