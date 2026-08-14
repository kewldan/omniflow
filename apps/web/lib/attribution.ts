/**
 * Carrying an advertisement's click identifier from the landing page to the
 * order that settles later.
 *
 * This is the part that could not be done at all before. An advertising
 * platform tags the click it sold — Google appends `gclid`, Yandex `yclid` —
 * and then wants to be told which of those clicks turned into money. Payment
 * here happens on the backend, often a day after the click and sometimes
 * through a transfer an operator confirms by hand, so the browser session that
 * carried the identifier is long gone by then. A counter script watching the
 * storefront sees the visit and never sees the sale.
 *
 * So the identifier is held, first-party, and attached to the order once the
 * order exists. It never leaves this origin except as that one attachment, and
 * this repository never sends anything to an advertising network — the operator
 * exports a file and uploads it themselves.
 *
 * Nothing here runs before consent. The capture is called from a component that
 * only mounts it after the visitor agreed, and declining clears whatever was
 * held.
 */

/** What the landing page captured. */
export type Attribution = {
  clickId?: string;
  clickSource?: string;
  source?: string;
  medium?: string;
  campaign?: string;
  content?: string;
  term?: string;
};

/**
 * The platforms that tag their own clicks, and the parameter each uses.
 *
 * A closed list. "Store whatever the URL carried" is how a session token, an
 * e-mail address, or somebody's password-reset link ends up in an analytics
 * table — the API refuses anything else anyway, and agreeing with it here means
 * the browser never holds what it could not send.
 */
const CLICK_PARAMETERS: Record<string, string> = {
  gclid: "google",
  gbraid: "google",
  wbraid: "google",
  yclid: "yandex",
  fbclid: "meta",
  msclkid: "microsoft",
  ttclid: "tiktok",
  twclid: "x",
};

/** The same shape the API validates against, so nothing is stored that it would refuse. */
const CLICK_IDENTIFIER = /^[A-Za-z0-9_.-]{6,200}$/;

const STORAGE_KEY = "omniflow.attribution";
const CHOICE_KEY = "omniflow.measurement";

/**
 * How long a click is carried.
 *
 * Thirty days is the window every major platform attributes over, and it is
 * also the answer to "how long should this sit in somebody's browser": long
 * enough that a customer who thinks about it for a week is still attributed,
 * short enough that it is not a permanent mark.
 */
const WINDOW_DAYS = 30;

type Stored = Attribution & { at: number };

/** Whether the visitor has agreed to measurement, and to this. */
export function readMeasurementChoice(): "unknown" | "granted" | "declined" {
  if (typeof window === "undefined") {
    return "unknown";
  }
  const stored = window.localStorage.getItem(CHOICE_KEY);
  return stored === "granted" || stored === "declined" ? stored : "unknown";
}

export function writeMeasurementChoice(granted: boolean) {
  if (typeof window !== "undefined") {
    window.localStorage.setItem(CHOICE_KEY, granted ? "granted" : "declined");
  }
}

/**
 * Reads the advertising parameters out of the current URL and holds them.
 *
 * Returns nothing when the visit carried none, which is the common case: most
 * visits are not from an advertisement, and storing something for every visit
 * would be a record of who arrived rather than of what an advertisement bought.
 *
 * First write wins. A customer who lands from an advertisement, browses, and
 * returns through a link somebody sent them was brought here by the
 * advertisement, and overwriting on the second visit would credit the friend.
 */
export function captureAttribution(search: string): Attribution | null {
  if (typeof window === "undefined") {
    return null;
  }
  const parameters = new URLSearchParams(search);
  const captured: Attribution = {};

  for (const [parameter, platform] of Object.entries(CLICK_PARAMETERS)) {
    const value = parameters.get(parameter)?.trim();
    if (value && CLICK_IDENTIFIER.test(value)) {
      captured.clickId = value;
      captured.clickSource = platform;
      break;
    }
  }
  captured.source = field(parameters.get("utm_source"));
  captured.medium = field(parameters.get("utm_medium"));
  captured.campaign = field(parameters.get("utm_campaign"));
  captured.content = field(parameters.get("utm_content"));
  captured.term = field(parameters.get("utm_term"));

  const anything = Object.values(captured).some((value) => value);
  if (!anything) {
    return readAttribution();
  }
  const existing = readAttribution();
  if (existing) {
    return existing;
  }
  const stored: Stored = { ...prune(captured), at: Date.now() };
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(stored));
  return prune(captured);
}

/** What is being carried, or null once it has aged out. */
export function readAttribution(): Attribution | null {
  if (typeof window === "undefined") {
    return null;
  }
  const raw = window.localStorage.getItem(STORAGE_KEY);
  if (!raw) {
    return null;
  }
  try {
    const stored = JSON.parse(raw) as Stored;
    if (!stored.at || Date.now() - stored.at > WINDOW_DAYS * 86_400_000) {
      window.localStorage.removeItem(STORAGE_KEY);
      return null;
    }
    const { at: _ignored, ...attribution } = stored;
    return Object.values(attribution).some((value) => value) ? attribution : null;
  } catch {
    window.localStorage.removeItem(STORAGE_KEY);
    return null;
  }
}

/** Forgets it. Called when somebody declines measurement, and after it is attached. */
export function clearAttribution() {
  if (typeof window !== "undefined") {
    window.localStorage.removeItem(STORAGE_KEY);
  }
}

/**
 * Bounds a campaign value the same way the API does, so the browser never holds
 * something the attachment would be refused for.
 */
function field(value: string | null): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed) {
    return undefined;
  }
  // The escapes are written out rather than the characters themselves, so this
  // source file stays free of the control bytes it is stripping.
  // biome-ignore lint/suspicious/noControlCharactersInRegex: removing them is the point.
  return trimmed.replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 120) || undefined;
}

function prune(attribution: Attribution): Attribution {
  return Object.fromEntries(
    Object.entries(attribution).filter(([, value]) => Boolean(value)),
  ) as Attribution;
}
