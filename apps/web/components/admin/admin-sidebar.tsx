"use client";

import { Badge } from "@omniflow/ui/badge";
import { cn } from "@omniflow/ui/lib/utils";
import { ScrollArea } from "@omniflow/ui/scroll-area";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTranslations } from "next-intl";

import { NAVIGATION, NAVIGATION_ITEMS } from "@/lib/navigation";
import { useSession } from "@/lib/session";

/**
 * Primary navigation.
 *
 * Rendered as a single `nav` landmark with a list per section, so a screen
 * reader announces the grouping and can jump straight here. The active entry
 * carries `aria-current="page"` in addition to its visual treatment.
 */
export function AdminSidebar({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const translate = useTranslations("admin");
  const { can } = useSession();

  // Only the deepest matching entry is current. Without this, a nested route
  // like /admin/settings/ai would highlight its parent as well, and two
  // "you are here" markers tell an operator nothing about where they are.
  const currentHref = NAVIGATION_ITEMS.filter((item) =>
    item.href === "/admin" ? pathname === "/admin" : pathname.startsWith(item.href),
  ).sort((left, right) => right.href.length - left.href.length)[0]?.href;

  return (
    <nav aria-label={translate("navigation.label")} className="flex h-full flex-col">
      <ScrollArea className="flex-1">
        <div className="flex flex-col gap-6 p-3">
          {NAVIGATION.map((section) => {
            const visible = section.items.filter(
              (item) => !item.permission || can(item.permission),
            );
            if (visible.length === 0) {
              return null;
            }
            return (
              <div className="flex flex-col gap-1" key={section.key}>
                <p className="px-2 pb-1 font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
                  {translate(`navigation.sections.${section.key}`)}
                </p>
                <ul className="flex flex-col gap-0.5">
                  {visible.map((item) => {
                    const active = item.href === currentHref;
                    const Icon = item.icon;
                    const label = translate(`navigation.items.${item.key}`);

                    if (item.planned) {
                      return (
                        <li key={item.key}>
                          <span
                            aria-disabled="true"
                            className="flex cursor-not-allowed items-center gap-2.5 rounded-md px-2 py-1.5 text-muted-foreground text-sm opacity-60"
                          >
                            <Icon aria-hidden="true" className="size-4 shrink-0" />
                            <span className="truncate">{label}</span>
                            <Badge className="ml-auto" variant="outline">
                              {translate("navigation.planned")}
                            </Badge>
                          </span>
                        </li>
                      );
                    }

                    return (
                      <li key={item.key}>
                        <Link
                          aria-current={active ? "page" : undefined}
                          className={cn(
                            "flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm transition-colors",
                            "focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none",
                            active
                              ? "bg-secondary font-medium text-foreground"
                              : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                          )}
                          href={item.href}
                          onClick={onNavigate}
                        >
                          <Icon aria-hidden="true" className="size-4 shrink-0" />
                          <span className="truncate">{label}</span>
                        </Link>
                      </li>
                    );
                  })}
                </ul>
              </div>
            );
          })}
        </div>
      </ScrollArea>
    </nav>
  );
}
