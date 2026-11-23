"use client";

import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTranslations } from "next-intl";

import { usePartnerState } from "@/components/account/account/partner-programme";

const PLACES = [
  { href: "/account/referrals", key: "invite" },
  { href: "/account/referrals/partner", key: "partner" },
] as const;

/**
 * The two referral places — inviting a friend, and the partner programme — as
 * a segmented switch at the top of both.
 *
 * They are two routes rather than two tabs of one page so each can be linked
 * to on its own (the bot and the hub send people straight to either) and so the
 * invite screen loads nothing of the partner one. The shell lights the profile
 * tab for both by prefix.
 *
 * The switch is drawn only when the installation runs a partner programme: a
 * one-segment switch would be a heading pretending to be a control.
 */
export function ReferralSwitch() {
  const translate = useTranslations("account.account.referralSwitch");
  const pathname = usePathname();
  const { data } = usePartnerState();

  if (!data && pathname === "/account/referrals") {
    return null;
  }

  return (
    <nav aria-label={translate("label")}>
      <ul className="grid grid-cols-2 gap-1 rounded-lg bg-secondary p-1">
        {PLACES.map((place) => {
          const active = pathname === place.href;
          return (
            <li key={place.href}>
              <Link
                aria-current={active ? "page" : undefined}
                className={cn(
                  "flex min-h-10 items-center justify-center rounded-md px-2 text-center",
                  "font-semibold text-[13.5px] leading-tight transition-colors",
                  "focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
                  active
                    ? "bg-card text-foreground shadow-xs"
                    : "text-muted-foreground hover:text-foreground",
                )}
                href={place.href}
                replace
              >
                {translate(place.key)}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
