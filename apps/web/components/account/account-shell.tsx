"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import {
  Check,
  ChevronLeft,
  Copy,
  Gift,
  Layers,
  LifeBuoy,
  LogOut,
  type LucideIcon,
  Megaphone,
  ReceiptText,
  RefreshCw,
  ShieldCheck,
  Smartphone,
  Store,
  Tag,
  UserPlus,
  UserRound,
  Wallet,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { type ReactNode, useEffect, useState } from "react";

import { AccountLiveUpdates } from "@/components/account/live-updates";
import { useMiniApp } from "@/components/account/miniapp";
import { AccountNotice } from "@/components/account/state";
import { NavIcon } from "@/components/admin/nav-icon";
import { useBrandName } from "@/components/brand-name";
import { LocaleSync } from "@/components/intl/locale-switch";
import { useAccount } from "@/lib/account-session";
import { apiFetch } from "@/lib/api";
import { flushReferralAttribution, rememberReferralCode } from "@/lib/referral";
import { signInPath } from "@/lib/sign-in-path";

/**
 * The tabs that carry the product.
 *
 * Five destinations, chosen by how often a customer needs each rather than by
 * how the API is divided. Buying a plan and buying digital goods are one
 * destination because from the customer's side they are the same errand, and
 * splitting them would spend a scarce tab on a catalogue many installations do
 * not sell at all. Devices moved off the bar and under the account tab: it is
 * something a person does once when a phone is replaced, not something they
 * come back to, and the subscription it belongs to links to it directly.
 *
 * The bar sizes its indicator from the count, so this list is the only place
 * that has to change when a destination is added or removed.
 */
const TABS: { href: string; icon: LucideIcon; key: string }[] = [
  { href: "/account", icon: Layers, key: "subscriptions" },
  { href: "/account/store", icon: Store, key: "store" },
  { href: "/account/wallet", icon: Wallet, key: "wallet" },
  { href: "/account/support", icon: LifeBuoy, key: "support" },
  { href: "/account/profile", icon: UserRound, key: "profile" },
];

/**
 * The tabs a Mini App in lite mode keeps: the errands a customer opens the bot's
 * Mini App for. Everything else stays reachable from the bot itself.
 */
const LITE_TABS = new Set(["subscriptions", "store", "support"]);

/**
 * The destinations a phone reaches through the account tab.
 *
 * On a narrow screen these stay one level down, because five is already the
 * ceiling for a thumb-reachable bar. A desktop sidebar has room to spare and no
 * reason to make somebody open a settings page to find their own orders, so it
 * lists them directly.
 */
const SECONDARY: { href: string; icon: LucideIcon; key: string }[] = [
  { href: "/account/orders", icon: ReceiptText, key: "orders" },
  { href: "/account/devices", icon: Smartphone, key: "devices" },
  { href: "/account/referrals", icon: UserPlus, key: "referrals" },
  { href: "/account/gifts", icon: Gift, key: "gifts" },
  { href: "/account/offers", icon: Tag, key: "offers" },
  { href: "/account/renewal", icon: RefreshCw, key: "renewal" },
  { href: "/account/news", icon: Megaphone, key: "news" },
  { href: "/account/security", icon: ShieldCheck, key: "security" },
];

/**
 * Section pages that open straight into their content with no heading of their
 * own. The shell names them with their navigation label, so every page has
 * one `<h1>` — what a screen reader lists first and what tells a desktop
 * visitor, who has no phone header, where they are. Pages that write their
 * own heading (support, news, preferences, a subscription) are not listed.
 */
const UNTITLED_SECTIONS: Record<string, string> = {
  "/account": "nav.subscriptions",
  "/account/devices": "nav.devices",
  "/account/orders": "nav.orders",
  "/account/privacy": "account.hub.privacy",
  "/account/profile": "nav.profile",
  "/account/referrals": "nav.referrals",
  "/account/gifts": "nav.gifts",
  "/account/security": "nav.security",
  "/account/wallet": "nav.wallet",
  "/account/renewal": "nav.renewal",
  "/account/offers": "nav.offers",
};

/** Routes that are reached from a tab rather than being one, so they show a back control. */
function isDetailRoute(pathname: string): boolean {
  return !TABS.some((tab) => tab.href === pathname);
}

/** The longest matching prefix, so a detail route keeps its section lit. */
function isActive(pathname: string, href: string): boolean {
  if (href === "/account") {
    return pathname === "/account";
  }
  return pathname === href || pathname.startsWith(`${href}/`);
}

/**
 * The customer panel's frame.
 *
 * Below `md` this is the phone layout it was built as: a sticky header, one
 * column, and a floating tab bar. At `md` and above it becomes a desktop
 * application — a persistent sidebar carrying every destination, and a content
 * column wide enough to use the screen. The earlier build served the phone
 * layout to every viewport, which put a 410px column and a thumb-sized tab bar
 * in the middle of a 1440px window; the customer opening the *web* panel rather
 * than the bot is, by definition, the one not on a phone.
 */
export function AccountShell({ children }: { children: ReactNode }) {
  const translate = useTranslations("account");
  const serviceName = useBrandName(translate("nav.title"));
  const { error, loading, session, signedOut, unavailable } = useAccount();
  const pathname = usePathname();
  const search = useSearchParams();
  const router = useRouter();
  const { inMiniApp, lite } = useMiniApp();
  const tabs = lite ? TABS.filter((tab) => LITE_TABS.has(tab.key)) : TABS;

  // Signing in is the only thing a signed-out visitor can do here, so the panel
  // sends them to do it instead of rendering a card whose single button does.
  // The path they were on travels along, so a customer whose session ended on
  // an order page comes back to that order rather than to the dashboard.
  useEffect(() => {
    if (signedOut) {
      const query = search.toString();
      router.replace(signInPath(query ? `${pathname}?${query}` : pathname));
    }
  }, [pathname, router, search, signedOut]);

  // A referral code carried through sign-in is attributed on the first render
  // that has a session, and an invite link that opened a panel page directly
  // is picked up here too. The helper forgets the code either way.
  useEffect(() => {
    rememberReferralCode(search.get("ref"));
    if (session) {
      void flushReferralAttribution();
    }
  }, [search, session]);

  if (loading || signedOut) {
    return <ShellSkeleton />;
  }
  if (unavailable) {
    return (
      <SignedOutNotice
        description={translate("states.unavailableDescription")}
        title={translate("states.unavailable")}
      />
    );
  }
  if (error || !session) {
    return (
      <SignedOutNotice
        action={<Button onClick={() => router.refresh()}>{translate("states.retry")}</Button>}
        description={translate("states.errorDescription")}
        title={translate("states.error")}
      />
    );
  }

  const showBack = isDetailRoute(pathname);
  const pageTitle = UNTITLED_SECTIONS[pathname];
  return (
    <div className="flex min-h-dvh flex-col bg-background md:flex-row">
      <LocaleSync preferred={session.customer.locale} />
      {/* Mounted only with a session: the stream answers 401 without one, and
          EventSource would retry it forever. */}
      <AccountLiveUpdates />
      <DesktopSidebar
        customerId={session.customer.id}
        lite={lite}
        pathname={pathname}
        tabs={tabs}
      />

      <div className="flex min-w-0 flex-1 flex-col">
        {/* A phone browser's title bar and way back. Inside Telegram neither is
            drawn: Telegram shows the Mini App's name itself, and its own back
            button (driven through tma.js) is the way back. */}
        {!inMiniApp && (
          <header className="sticky top-[var(--miniapp-top,0px)] z-20 flex items-center gap-1 border-chrome-border border-b bg-chrome px-3 py-2 md:hidden">
            {showBack ? (
              <Button
                className="-ml-1 h-8 gap-1 px-2"
                onClick={() => router.back()}
                size="sm"
                variant="ghost"
              >
                <ChevronLeft aria-hidden />
                {translate("nav.back")}
              </Button>
            ) : (
              <span className="w-8" />
            )}
            <div className="flex flex-1 flex-col items-center">
              <span className="font-semibold text-[15px] tracking-[-0.01em]">{serviceName}</span>
              <span className="font-mono text-[10.5px] text-subtle-foreground">
                {translate("nav.subtitle")}
              </span>
            </div>
            <span className="w-8" />
          </header>
        )}

        {/* The page is a column on a phone — content, then the sticky tab bar,
            which takes its own room at the end — so it is the screen's height
            and no more, and the content needs only a little air above the bar. */}
        <main className="mx-auto w-full max-w-lg flex-1 px-4 pt-3 pb-4 md:max-w-3xl md:px-8 md:pt-8 md:pb-16">
          {pageTitle ? (
            <h1 className="mb-4 font-semibold text-[19px] tracking-[-0.02em]">
              {translate(pageTitle)}
            </h1>
          ) : null}
          {children}
        </main>
      </div>

      <TabBar pathname={pathname} tabs={tabs} />
    </div>
  );
}

/**
 * The desktop navigation.
 *
 * It carries the same five destinations as the bar plus the ones a phone hides
 * behind the account tab, because the constraint that justified hiding them —
 * five is all a thumb bar can hold — does not apply to a column with room left
 * over.
 */
function DesktopSidebar({
  customerId,
  lite,
  pathname,
  tabs,
}: {
  customerId: string;
  lite: boolean;
  pathname: string;
  tabs: typeof TABS;
}) {
  const translate = useTranslations("account");
  const serviceName = useBrandName(translate("nav.title"));

  return (
    <aside className="sticky top-0 hidden h-dvh w-64 shrink-0 flex-col border-chrome-border border-r bg-chrome md:flex">
      <div className="flex flex-col gap-0.5 px-5 pt-6 pb-5">
        <span className="font-semibold text-[15px] tracking-[-0.01em]">{serviceName}</span>
        <span className="font-mono text-[10.5px] text-subtle-foreground">
          {translate("nav.subtitle")}
        </span>
      </div>

      <nav aria-label={translate("nav.tabs")} className="flex-1 overflow-y-auto px-3 pb-4">
        <ul className="flex flex-col gap-0.5">
          {tabs.map((tab) => (
            <SidebarLink
              animation={tab.key}
              href={tab.href}
              icon={tab.icon}
              key={tab.href}
              label={translate(`nav.${tab.key}`)}
              pathname={pathname}
            />
          ))}
        </ul>

        {lite ? null : (
          <p className="px-3 pt-5 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
            {translate("nav.more")}
          </p>
        )}
        <ul className="flex flex-col gap-0.5">
          {(lite ? [] : SECONDARY).map((item) => (
            <SidebarLink
              animation={item.key}
              href={item.href}
              icon={item.icon}
              key={item.href}
              label={translate(`nav.${item.key}`)}
              pathname={pathname}
            />
          ))}
        </ul>
      </nav>

      <AccountFooter customerId={customerId} />
    </aside>
  );
}

function SidebarLink({
  animation,
  href,
  icon: Icon,
  label,
  pathname,
}: {
  /** The entry's animation under /lottie/account, played on hover and tap. */
  animation?: string;
  href: string;
  icon?: LucideIcon;
  label: string;
  pathname: string;
}) {
  const active = isActive(pathname, href);
  const [play, setPlay] = useState(0);
  return (
    <li>
      <Link
        aria-current={active ? "page" : undefined}
        className={cn(
          "flex items-center gap-2.5 rounded-md px-3 py-2 text-[13.5px] transition-colors",
          "focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
          active
            ? "bg-accent font-medium text-foreground"
            : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
        )}
        href={href}
        onClick={() => setPlay((count) => count + 1)}
        onMouseEnter={() => setPlay((count) => count + 1)}
      >
        {Icon ? (
          <NavIcon animation={animation} icon={Icon} play={play} set="account" />
        ) : (
          <span aria-hidden className="size-4" />
        )}
        {label}
      </Link>
    </li>
  );
}

/**
 * Who is signed in, and the way out.
 *
 * The identifier is here because it is the one thing support will ask for and
 * the panel never used to show. It is shown by its ends, which are enough to
 * recognise it, and a tap copies all of it, which is how it reaches support.
 */
function AccountFooter({ customerId }: { customerId: string }) {
  const translate = useTranslations("account");
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [copied, setCopied] = useState(false);

  return (
    <div className="border-chrome-border border-t px-3 py-3">
      <div className="flex items-start gap-2.5 px-2 pb-2">
        <AccountPicture />
        <button
          className="group min-w-0 rounded-md text-left focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
          onClick={() => {
            void navigator.clipboard?.writeText(customerId).then(
              () => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 2000);
              },
              () => undefined,
            );
          }}
          title={customerId}
          type="button"
        >
          <p className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
            {translate(copied ? "nav.copied" : "nav.accountId")}
          </p>
          <p className="flex items-center gap-1.5 font-mono text-[10.5px] text-muted-foreground group-hover:text-foreground">
            {`${customerId.slice(0, 8)}…${customerId.slice(-7)}`}
            {copied ? (
              <Check aria-hidden className="size-3 text-success" />
            ) : (
              <Copy aria-hidden className="size-3 opacity-60" />
            )}
          </p>
          {/* Said after the visible text, so the button's name starts with what
              is on screen and still says what pressing it does. */}
          <span className="sr-only">{translate("nav.copyAccountId")}</span>
        </button>
      </div>
      <Button
        className="w-full justify-start gap-2.5 px-3 text-[13.5px] text-muted-foreground"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setFailed(false);
          try {
            await apiFetch("/v1/account/auth/logout", { method: "POST" });
            router.replace("/account/sign-in?signedOut=1");
          } catch {
            // Reported in place rather than through the toaster: this component
            // is in every account route's bundle, and pulling the toast runtime
            // into all of them for one failure path costs more than it explains.
            setFailed(true);
            setBusy(false);
          }
        }}
        size="sm"
        variant="ghost"
      >
        <LogOut aria-hidden className="size-4" />
        {translate("nav.signOut")}
      </Button>
      {failed && (
        <p className="px-3 pt-1.5 text-[11px] text-destructive" role="alert">
          {translate("nav.signOutFailed")}
        </p>
      )}
    </div>
  );
}

function TabBar({ pathname, tabs }: { pathname: string; tabs: typeof TABS }) {
  const translate = useTranslations("account");
  // How many times each tab was tapped; every change plays its icon once.
  const [plays, setPlays] = useState<Record<string, number>>({});
  // The active tab is the longest matching prefix, so a detail route under a tab
  // keeps that tab lit rather than falling back to the first one.
  const activeIndex = tabs.reduce((best, tab, index) => {
    const matches = pathname === tab.href || pathname.startsWith(`${tab.href}/`);
    return matches && tab.href.length >= tabs[best].href.length ? index : best;
  }, 0);

  return (
    <nav
      aria-label={translate("nav.tabs")}
      className="pointer-events-none sticky bottom-0 z-10 px-4 pt-2 pb-6 md:hidden"
    >
      <div className="pointer-events-auto relative mx-auto flex max-w-lg rounded-full border border-[color:var(--glass-border)] bg-[color:var(--glass)] p-1.5 shadow-[var(--glass-shadow)] backdrop-blur-[22px] backdrop-saturate-150">
        {/* The travelling pill. It is decorative, so it is hidden from assistive
            technology — the current tab is announced by aria-current instead. */}
        <span
          aria-hidden
          className="absolute top-1.5 bottom-1.5 left-1.5 rounded-full bg-[color:var(--glass-pill)] shadow-[inset_0_1px_0_rgb(255_255_255/0.18)] transition-transform duration-[440ms] ease-[cubic-bezier(0.22,1,0.3,1)] motion-reduce:transition-none"
          style={{
            transform: `translateX(${activeIndex * 100}%)`,
            width: `calc((100% - 0.75rem) / ${tabs.length})`,
          }}
        />
        {tabs.map((tab, index) => {
          const Icon = tab.icon;
          const active = index === activeIndex;
          return (
            <Link
              aria-current={active ? "page" : undefined}
              className={cn(
                "relative flex flex-1 flex-col items-center gap-1 rounded-full py-1.5 transition-colors",
                "focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
                active ? "text-foreground" : "text-subtle-foreground",
              )}
              href={tab.href}
              key={tab.href}
              onClick={() => setPlays((all) => ({ ...all, [tab.key]: (all[tab.key] ?? 0) + 1 }))}
            >
              <NavIcon
                animation={tab.key}
                className="size-[19px]"
                icon={Icon}
                play={plays[tab.key] ?? 0}
                set="account"
              />
              <span className="font-medium font-mono text-[9.5px]">
                {translate(`nav.${tab.key}`)}
              </span>
            </Link>
          );
        })}
      </div>
    </nav>
  );
}

/** The frame while the session is still loading, so the page does not jump. */
function ShellSkeleton() {
  return (
    <div className="min-h-dvh bg-background md:flex">
      <div className="hidden h-dvh w-64 shrink-0 border-chrome-border border-r bg-chrome md:block" />
      <div className="flex min-h-dvh flex-1 flex-col">
        <div className="h-14 border-chrome-border border-b bg-chrome md:hidden" />
        <div className="mx-auto w-full max-w-lg flex-1 space-y-3 px-4 pt-4 md:max-w-3xl md:px-8 md:pt-8">
          <div className="h-16 animate-pulse rounded-lg bg-card" />
          <div className="h-40 animate-pulse rounded-lg bg-card" />
          <div className="h-40 animate-pulse rounded-lg bg-card" />
        </div>
      </div>
    </div>
  );
}

function SignedOutNotice({
  action,
  description,
  title,
}: {
  action?: ReactNode;
  description: string;
  title: string;
}) {
  return (
    <main className="mx-auto flex min-h-dvh max-w-lg items-center px-6">
      <AccountNotice action={action} description={description} title={title} variant="forbidden" />
    </main>
  );
}

/**
 * The customer's Telegram photo, served by the installation (Telegram's own
 * picture hosts are not reachable everywhere). Nothing is drawn for a
 * customer without one.
 */
function AccountPicture() {
  const [failed, setFailed] = useState(false);
  if (failed) {
    return null;
  }
  return (
    // biome-ignore lint/performance/noImgElement: a private, same-origin photo the API serves
    <img
      alt=""
      className="mt-0.5 size-8 shrink-0 rounded-full object-cover"
      height={32}
      onError={() => setFailed(true)}
      src="/v1/account/avatar"
      width={32}
    />
  );
}
