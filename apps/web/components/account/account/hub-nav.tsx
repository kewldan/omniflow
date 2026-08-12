"use client";

import { ChevronRight } from "lucide-react";
import Link from "next/link";
import type { ComponentType } from "react";

/** One destination in a hub group. */
export type HubNavItem = {
  description?: string;
  href: string;
  icon: ComponentType<{ className?: string }>;
  label: string;
};

/**
 * A grouped list of links out of the account screen.
 *
 * The customer panel has five tabs and more than five destinations, so
 * everything that is used rarely — devices, sessions, invites, personal data —
 * is reached from here instead. It is one card with hairline rules rather than
 * separate buttons because the group reads as a menu, and a menu is scanned
 * downwards.
 *
 * The whole row is the target rather than the label alone: on a phone the
 * difference between a 44px row and a 20px string of text is whether the link
 * can be hit at all.
 */
export function HubNav({ items, label }: { items: HubNavItem[]; label: string }) {
  return (
    <nav aria-label={label}>
      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {items.map((item) => {
          const Icon = item.icon;
          return (
            <li key={item.href}>
              <Link
                className="flex items-center gap-3 px-4 py-3.5 transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
                href={item.href}
              >
                <Icon aria-hidden className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block font-semibold text-[15px]">{item.label}</span>
                  {item.description && (
                    <span className="mt-0.5 block text-[12px] text-subtle-foreground leading-snug">
                      {item.description}
                    </span>
                  )}
                </span>
                <ChevronRight aria-hidden className="size-4 shrink-0 text-subtle-foreground" />
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
