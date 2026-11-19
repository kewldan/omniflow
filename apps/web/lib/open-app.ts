/**
 * The application links the /open page will forward a browser to.
 *
 * Telegram's inline buttons accept only http, https, and tg addresses, so the
 * bot cannot offer `happ://…` as a button. It offers `/open#<link>` instead, and
 * this page forwards the browser to the application. The link travels in the
 * fragment, which a browser never sends to a server.
 *
 * Only an application scheme is followed. A web address would turn the page
 * into an open redirect, and `javascript:` and its neighbours would run in this
 * origin.
 */
const APP_LINK = /^[a-z][a-z0-9+.-]{1,30}:\/\/\S+$/i;
const REFUSED = new Set([
  "http",
  "https",
  "javascript",
  "data",
  "vbscript",
  "file",
  "blob",
  "about",
]);
const MAX_LENGTH = 4096;

/** Decodes the page's fragment into the link it carries, or "" when it is not one. */
export function appLinkFromHash(hash: string): string {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  if (!raw || raw.length > MAX_LENGTH * 3) {
    return "";
  }
  let decoded: string;
  try {
    decoded = decodeURIComponent(raw.replace(/\+/g, "%20"));
  } catch {
    return "";
  }
  return isAppLink(decoded) ? decoded : "";
}

/** Reports whether a value is an application link the page may open. */
export function isAppLink(value: string): boolean {
  if (value.length > MAX_LENGTH || !APP_LINK.test(value)) {
    return false;
  }
  const scheme = value.slice(0, value.indexOf(":")).toLowerCase();
  return !REFUSED.has(scheme);
}
