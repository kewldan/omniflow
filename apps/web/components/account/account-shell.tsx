"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import {
  ChevronLeft,
  Layers,
  LifeBuoy,
  LogOut,
  type LucideIcon,
  Store,
  UserRound,
  Wallet,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { type ReactNode, useEffect, useState } from "react";

import { AccountNotice } from "@/components/account/state";
import { useAccount } from "@/lib/account-session";
import { apiFetch } from "@/lib/api";
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
 * The destinations a phone reaches through the account tab.
 *
 * On a narrow screen these stay one level down, because five is already the
 * ceiling for a thumb-reachable bar. A desktop sidebar has room to spare and no
 * reason to make somebody open a settings page to find their own orders, so it
 * lists them directly.
 */
const SECONDARY: { href: string; key: string }[] = [
  { href: "/account/orders", key: "orders" },
  { href: "/account/devices", key: "devices" },
  { href: "/account/referrals", key: "referrals" },
  { href: "/account/news", key: "news" },
  { href: "/account/security", key: "security" },
];

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
  const { error, loading, session, signedOut, unavailable } = useAccount();
  const pathname = usePathname();
  const search = useSearchParams();
  const router = useRouter();

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
  return (
    <div className="min-h-dvh bg-background md:flex">
      <DesktopSidebar customerId={session.customer.id} pathname={pathname} />

      <div className="flex min-h-dvh flex-1 flex-col md:min-h-0">
        <header className="sticky top-0 z-20 flex items-center gap-1 border-chrome-border border-b bg-chrome px-3 py-2 md:hidden">
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
            <span className="font-semibold text-[15px] tracking-[-0.01em]">
              {translate("nav.title")}
            </span>
            <span className="font-mono text-[10.5px] text-subtle-foreground">
              {translate("nav.subtitle")}
            </span>
          </div>
          <span className="w-8" />
        </header>

        {/* The bottom padding clears the floating tab bar on a phone. There is no
            bar on desktop, so there is nothing to clear and the padding goes. */}
        <main className="mx-auto w-full max-w-lg flex-1 px-4 pt-3 pb-32 md:max-w-3xl md:px-8 md:pt-8 md:pb-16">
          {children}
        </main>
      </div>

      <TabBar pathname={pathname} />
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
function DesktopSidebar({ customerId, pathname }: { customerId: string; pathname: string }) {
  const translate = useTranslations("account");

  return (
    <aside className="sticky top-0 hidden h-dvh w-64 shrink-0 flex-col border-chrome-border border-r bg-chrome md:flex">
      <div className="flex flex-col gap-0.5 px-5 pt-6 pb-5">
        <span className="font-semibold text-[15px] tracking-[-0.01em]">
          {translate("nav.title")}
        </span>
        <span className="font-mono text-[10.5px] text-subtle-foreground">
          {translate("nav.subtitle")}
        </span>
      </div>

      <nav aria-label={translate("nav.tabs")} className="flex-1 overflow-y-auto px-3 pb-4">
        <ul className="flex flex-col gap-0.5">
          {TABS.map((tab) => (
            <SidebarLink
              href={tab.href}
              icon={tab.icon}
              key={tab.href}
              label={translate(`nav.${tab.key}`)}
              pathname={pathname}
            />
          ))}
        </ul>

        <p className="px-3 pt-5 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
          {translate("nav.more")}
        </p>
        {/* The secondary group carries no icons. Five more glyphs would cost
            every account route their bytes for a list whose labels already say
            what each destination is — the shell is in every bundle, so what it
            imports is charged to screens that never render it. */}
        <ul className="flex flex-col gap-0.5">
          {SECONDARY.map((item) => (
            <SidebarLink
              href={item.href}
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
  href,
  icon: Icon,
  label,
  pathname,
}: {
  href: string;
  icon?: LucideIcon;
  label: string;
  pathname: string;
}) {
  const active = isActive(pathname, href);
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
      >
        {Icon ? <Icon aria-hidden className="size-4" /> : <span aria-hidden className="size-4" />}
        {label}
      </Link>
    </li>
  );
}

/**
 * Who is signed in, and the way out.
 *
 * The identifier is here because it is the one thing support will ask for and
 * the panel never used to show. It is rendered whole rather than truncated: a
 * shortened reference that has to be expanded before it can be quoted is not an
 * identifier, it is a hint.
 */
function AccountFooter({ customerId }: { customerId: string }) {
  const translate = useTranslations("account");
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  return (
    <div className="border-chrome-border border-t px-3 py-3">
      <div className="px-2 pb-2">
        <p className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
          {translate("nav.accountId")}
        </p>
        <p className="break-all font-mono text-[10.5px] text-muted-foreground">{customerId}</p>
      </div>
      <Button
        className="w-full justify-start gap-2.5 px-3 text-[13.5px] text-muted-foreground"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setFailed(false);
          try {
            await apiFetch("/v1/account/auth/logout", { method: "POST" });
            router.replace("/account/sign-in");
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

function TabBar({ pathname }: { pathname: string }) {
  const translate = useTranslations("account");
  // The active tab is the longest matching prefix, so a detail route under a tab
  // keeps that tab lit rather than falling back to the first one.
  const activeIndex = TABS.reduce((best, tab, index) => {
    const matches = pathname === tab.href || pathname.startsWith(`${tab.href}/`);
    return matches && tab.href.length >= TABS[best].href.length ? index : best;
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
            width: `calc((100% - 0.75rem) / ${TABS.length})`,
          }}
        />
        {TABS.map((tab, index) => {
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
            >
              <Icon aria-hidden className="size-[19px]" />
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
