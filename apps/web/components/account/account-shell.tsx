"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { ChevronLeft, Layers, MonitorSmartphone, UserRound } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { ComponentType, ReactNode } from "react";

import { AccountNotice } from "@/components/account/state";
import { useAccount } from "@/lib/account-session";

/**
 * The tabs that carry the product.
 *
 * The source design has four, the fourth being the wallet. Wallet, checkout, and
 * order history are v0.10 work, and a tab that leads nowhere is worse than a tab
 * that is not there yet — so this ships three and the bar sizes its indicator
 * from the count rather than from a hard-coded quarter.
 */
const TABS: { href: string; icon: ComponentType<{ className?: string }>; key: string }[] = [
  { href: "/account", icon: Layers, key: "subscriptions" },
  { href: "/account/devices", icon: MonitorSmartphone, key: "devices" },
  { href: "/account/profile", icon: UserRound, key: "profile" },
];

/** Routes that are reached from a tab rather than being one, so they show a back control. */
function isDetailRoute(pathname: string): boolean {
  return !TABS.some((tab) => tab.href === pathname);
}

/**
 * The customer panel's frame: a sticky header, the scrolling page, and the
 * floating tab bar.
 *
 * Mobile-first is not a slogan here — the layout is a single column that a
 * wider viewport centres rather than a desktop layout that collapses. The
 * customer product is opened on a phone, frequently from inside Telegram.
 */
export function AccountShell({ children }: { children: ReactNode }) {
  const translate = useTranslations("account");
  const { error, loading, session, signedOut, unavailable } = useAccount();
  const pathname = usePathname();
  const router = useRouter();

  if (loading) {
    return <ShellSkeleton />;
  }
  if (signedOut) {
    return (
      <SignedOutNotice
        action={
          <Button asChild>
            <Link href="/account/sign-in">{translate("signIn.action")}</Link>
          </Button>
        }
        description={translate("states.signedOutDescription")}
        title={translate("states.signedOut")}
      />
    );
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
    <div className="flex min-h-dvh flex-col bg-background">
      <header className="sticky top-0 z-20 flex items-center gap-1 border-chrome-border border-b bg-chrome px-3 py-2">
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

      {/* The bottom padding clears the floating tab bar so the last row of a
          long page is never trapped underneath it. */}
      <main className="mx-auto w-full max-w-lg flex-1 px-4 pt-3 pb-32">{children}</main>

      <TabBar pathname={pathname} />
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
      className="pointer-events-none sticky bottom-0 z-10 px-4 pt-2 pb-6"
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
    <div className="flex min-h-dvh flex-col bg-background">
      <div className="h-14 border-chrome-border border-b bg-chrome" />
      <div className="mx-auto w-full max-w-lg flex-1 space-y-3 px-4 pt-4">
        <div className="h-16 animate-pulse rounded-lg bg-card" />
        <div className="h-40 animate-pulse rounded-lg bg-card" />
        <div className="h-40 animate-pulse rounded-lg bg-card" />
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
