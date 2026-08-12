"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { BellOff, BellRing } from "lucide-react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useCallback, useEffect, useRef, useState } from "react";
import useSWR from "swr";

import { fetcher } from "@/lib/api";

import { NEWS_KEY, type NewsPage, SUPPORT_TICKETS_KEY, type SupportTicketPage } from "./types";

/**
 * Browser notifications, deliberately scoped to the foreground.
 *
 * What this is: the Notification API, driven by data this tab is already
 * polling. What it is explicitly not: web push. There is no service worker, no
 * VAPID key, and no subscription sent anywhere, because Omniflow has no
 * server-side push to deliver one — a subscription the server never uses is a
 * permission prompt spent on nothing. When the tab is closed, Telegram is what
 * reaches the customer, which is the channel the product already guarantees.
 *
 * Permission is never requested on load. The browser gives a site exactly one
 * ungrudging chance to ask, and asking before the customer has said they want
 * notifications is how that chance is wasted: a reflexive "Block" is permanent
 * and cannot be undone from the page. So the prompt only ever follows a click on
 * a control that says what it will do.
 */

/**
 * The opt-in lives in localStorage rather than in the account.
 *
 * Permission is granted per browser, so an account-level flag would claim a
 * setting one device cannot honour. A customer who turns this on at a desk and
 * opens the panel on a phone has, correctly, not turned it on there.
 */
const STORAGE_KEY = "omniflow.account.browser-notifications";

/**
 * How often the watcher looks for something to announce.
 *
 * A minute is chosen against the alternative rather than for its own sake:
 * anything shorter is a poll of two endpoints per tab per interval for a message
 * that is rarely urgent, and anything longer stops feeling like a notification.
 */
const POLL_INTERVAL_MS = 60_000;

export type NotificationState = "unsupported" | "default" | "granted" | "denied";

function readPermission(): NotificationState {
  if (typeof window === "undefined" || !("Notification" in window)) {
    return "unsupported";
  }
  return Notification.permission;
}

export type BrowserNotifications = {
  /** `null` until the first client effect has read the browser, so nothing renders differently on the server. */
  state: NotificationState | null;
  /** Permission is granted and the customer has asked for notifications in this browser. */
  optedIn: boolean;
  enable: () => Promise<NotificationState>;
  disable: () => void;
  notify: (options: { body: string; onOpen?: () => void; tag: string; title: string }) => void;
};

export function useBrowserNotifications(): BrowserNotifications {
  const [state, setState] = useState<NotificationState | null>(null);
  const [optedIn, setOptedIn] = useState(false);

  useEffect(() => {
    const permission = readPermission();
    setState(permission);
    // Both halves have to agree. A permission revoked in the browser's own
    // settings leaves the stored opt-in behind, and honouring that stale flag
    // would show "on" for something that can no longer fire.
    setOptedIn(permission === "granted" && window.localStorage.getItem(STORAGE_KEY) === "on");
  }, []);

  const enable = useCallback(async () => {
    if (!("Notification" in window)) {
      setState("unsupported");
      return "unsupported" as const;
    }
    // A denied permission is not re-requested: the browser answers instantly
    // from its own record, and calling anyway would let the page pretend it
    // asked. The screen says so instead.
    const permission =
      Notification.permission === "default"
        ? ((await Notification.requestPermission()) as NotificationState)
        : (Notification.permission as NotificationState);
    setState(permission);
    if (permission !== "granted") {
      window.localStorage.removeItem(STORAGE_KEY);
      setOptedIn(false);
      return permission;
    }
    window.localStorage.setItem(STORAGE_KEY, "on");
    setOptedIn(true);
    return permission;
  }, []);

  const disable = useCallback(() => {
    // Only the opt-in is dropped. A granted permission is the browser's to hold,
    // and there is no API to hand it back — pretending otherwise would leave the
    // customer looking for an "off" that did nothing.
    window.localStorage.removeItem(STORAGE_KEY);
    setOptedIn(false);
  }, []);

  const notify = useCallback(
    (options: { body: string; onOpen?: () => void; tag: string; title: string }) => {
      if (!optedIn || readPermission() !== "granted") {
        return;
      }
      // Nothing is raised while the customer is looking at the page. A desktop
      // notification for a reply that just appeared on screen is noise, and the
      // whole point of the feature is the tab they left in the background.
      if (document.visibilityState === "visible" && document.hasFocus()) {
        return;
      }
      try {
        const notification = new Notification(options.title, {
          body: options.body,
          // The tag collapses repeats, so a conversation that updates twice
          // before anybody looks leaves one notification rather than a stack.
          tag: options.tag,
        });
        notification.onclick = () => {
          window.focus();
          notification.close();
          options.onOpen?.();
        };
      } catch {
        // Chrome on Android refuses the constructor outright: notifications there
        // exist only through a service worker, which this feature deliberately
        // does not have. Recording it as unsupported turns a silent no-op into an
        // explanation on the settings screen.
        setState("unsupported");
      }
    },
    [optedIn],
  );

  return { disable, enable, notify, optedIn, state };
}

/**
 * The opt-in control, shown on the notification-preferences screen.
 *
 * Every outcome the browser can be in has its own sentence, because they need
 * different things from the customer: "not asked yet" needs a click, "denied"
 * needs a trip into browser settings that this page cannot make for them, and
 * "unsupported" needs reassurance that nothing was lost.
 */
export function BrowserNotificationSetting() {
  const translate = useTranslations("account.support");
  const { disable, enable, optedIn, state } = useBrowserNotifications();
  const [busy, setBusy] = useState(false);

  async function turnOn() {
    setBusy(true);
    try {
      const permission = await enable();
      if (permission === "granted") {
        toast.success(translate("browser.granted"));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-start gap-3">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted">
          {optedIn ? (
            <BellRing aria-hidden className="size-[15px] text-muted-foreground" />
          ) : (
            <BellOff aria-hidden className="size-[15px] text-muted-foreground" />
          )}
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="font-semibold text-[15px]">{translate("browser.title")}</h3>
          <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
            {translate("browser.description")}
          </p>
        </div>
      </div>

      {/* Nothing about the browser is known during server rendering, so the
          status line stays absent until the first effect has read it rather than
          guessing at a state and correcting itself on hydration. */}
      {state === null ? null : state === "unsupported" ? (
        <p className="text-[12.5px] text-muted-foreground leading-relaxed" role="status">
          <span className="font-medium text-foreground">{translate("browser.unsupported")}</span>{" "}
          {translate("browser.unsupportedDescription")}
        </p>
      ) : state === "denied" ? (
        <p
          className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2.5 text-[12.5px] leading-relaxed"
          role="status"
        >
          <span className="font-medium">{translate("browser.denied")}</span>{" "}
          {translate("browser.deniedDescription")}
        </p>
      ) : (
        <div className="space-y-3">
          <p className="text-[12.5px] text-muted-foreground leading-relaxed" role="status">
            {optedIn ? translate("browser.enabled") : translate("browser.off")}
          </p>
          {optedIn ? (
            <Button
              onClick={() => {
                disable();
                toast.success(translate("browser.dismissed"));
              }}
              size="sm"
              variant="outline"
            >
              {translate("browser.disable")}
            </Button>
          ) : (
            <Button disabled={busy} onClick={turnOn} size="sm">
              {translate("browser.enable")}
            </Button>
          )}
        </div>
      )}
    </section>
  );
}

/**
 * The watcher that turns a poll into a notification.
 *
 * It compares each conversation's unread count against the last count this tab
 * saw. That figure is the server's, computed from the message record the bot
 * shares, so a reply already read in Telegram arrives here with nothing to
 * announce — which is the behaviour the shared read state is for, and the reason
 * this component keeps no idea of its own about what has been "seen".
 *
 * The first observation only establishes a baseline. Without that, opening the
 * panel with three unread replies would fire three notifications for messages
 * that are already on screen.
 *
 * It renders nothing and fetches nothing until the customer has opted in, and it
 * shares SWR keys with the screens it sits on, so mounting it costs no extra
 * request while those screens are open.
 */
export function SupportAlerts() {
  const translate = useTranslations("account.support");
  const router = useRouter();
  const { notify, optedIn } = useBrowserNotifications();
  const { data: tickets } = useSWR<SupportTicketPage>(
    optedIn ? SUPPORT_TICKETS_KEY : null,
    fetcher,
    {
      refreshInterval: POLL_INTERVAL_MS,
    },
  );
  const { data: news } = useSWR<NewsPage>(optedIn ? NEWS_KEY : null, fetcher, {
    refreshInterval: POLL_INTERVAL_MS,
  });
  const seenReplies = useRef<Map<string, number> | null>(null);
  const seenPosts = useRef<Set<string> | null>(null);

  useEffect(() => {
    // Turning the feature off drops the baseline, so turning it back on later
    // announces what arrives from then rather than replaying the gap.
    if (!optedIn) {
      seenReplies.current = null;
      seenPosts.current = null;
    }
  }, [optedIn]);

  useEffect(() => {
    if (!tickets) {
      return;
    }
    const counts = new Map(tickets.items.map((ticket) => [ticket.id, ticket.unreadCount]));
    const previous = seenReplies.current;
    seenReplies.current = counts;
    if (!previous) {
      return;
    }
    for (const ticket of tickets.items) {
      if (ticket.unreadCount > (previous.get(ticket.id) ?? 0)) {
        notify({
          body: ticket.subject,
          onOpen: () => router.push(`/account/support/${ticket.id}`),
          tag: `omniflow-ticket-${ticket.id}`,
          title: translate("browser.replyTitle"),
        });
      }
    }
  }, [notify, router, tickets, translate]);

  useEffect(() => {
    if (!news) {
      return;
    }
    const ids = new Set(news.items.map((item) => item.id));
    const previous = seenPosts.current;
    seenPosts.current = ids;
    if (!previous) {
      return;
    }
    for (const item of news.items) {
      if (!previous.has(item.id) && !item.read) {
        notify({
          body: item.title,
          onOpen: () => router.push("/account/news"),
          tag: `omniflow-news-${item.id}`,
          title: translate("browser.newsTitle"),
        });
      }
    }
  }, [news, notify, router, translate]);

  return null;
}
