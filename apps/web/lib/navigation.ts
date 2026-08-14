import {
  Activity,
  ChartColumn,
  ClipboardList,
  CreditCard,
  Gift,
  LayoutDashboard,
  LifeBuoy,
  type LucideIcon,
  Megaphone,
  Package,
  Server,
  ShieldAlert,
  ShieldCheck,
  ShoppingBag,
  SlidersHorizontal,
  Sparkles,
  UserCog,
  Users,
} from "lucide-react";

/**
 * The panel's navigation model.
 *
 * `permission` is the capability that makes an entry reachable. An entry the
 * operator cannot use is hidden rather than shown-and-disabled, because a
 * disabled control invites repeated clicking with no way to resolve it. The API
 * enforces the same permission regardless of what is rendered.
 *
 * `messageKey` indexes the next-intl catalogue rather than carrying literal
 * copy, so every label exists in both Russian and English.
 */
export type NavigationItem = {
  key: string;
  href: string;
  icon: LucideIcon;
  permission?: string;
  /** Entries for surfaces that arrive in a later version render as disabled. */
  planned?: boolean;
};

export type NavigationSection = {
  key: string;
  items: NavigationItem[];
};

export const NAVIGATION: NavigationSection[] = [
  {
    key: "overview",
    items: [{ key: "dashboard", href: "/admin", icon: LayoutDashboard }],
  },
  {
    key: "operations",
    items: [
      {
        key: "customers",
        href: "/admin/customers",
        icon: Users,
        permission: "customers.read",
      },
      {
        key: "finance",
        href: "/admin/finance",
        icon: CreditCard,
        permission: "finance.read",
      },
      {
        key: "reports",
        href: "/admin/reports",
        icon: ChartColumn,
        permission: "finance.read",
      },
      {
        key: "traffic",
        href: "/admin/traffic",
        icon: Activity,
        permission: "customers.read",
      },
      {
        key: "catalog",
        href: "/admin/catalog",
        icon: Package,
        permission: "catalog.read",
      },
      {
        key: "shop",
        href: "/admin/shop",
        icon: ShoppingBag,
        permission: "goods.read",
      },
      {
        key: "gifts",
        href: "/admin/gifts",
        icon: Gift,
        permission: "gifts.read",
      },
      {
        key: "offers",
        href: "/admin/offers",
        icon: Megaphone,
        permission: "marketing.read",
      },
      {
        key: "support",
        href: "/admin/support",
        icon: LifeBuoy,
        permission: "support.read",
      },
      {
        key: "marketing",
        href: "/admin/marketing",
        icon: Megaphone,
        permission: "marketing.read",
      },
    ],
  },
  {
    key: "governance",
    items: [
      { key: "risk", href: "/admin/risk", icon: ShieldAlert, permission: "risk.read" },
      { key: "operators", href: "/admin/operators", icon: UserCog, permission: "admins.read" },
      { key: "audit", href: "/admin/audit", icon: ClipboardList, permission: "audit.read" },
      { key: "system", href: "/admin/system", icon: Server, permission: "system.read" },
      {
        key: "settings",
        href: "/admin/settings",
        icon: SlidersHorizontal,
        permission: "settings.read",
      },
      // The AI area keeps its own entry because it is the one an operator goes
      // to repeatedly. The other four are reached through the settings index,
      // which exists so the sidebar does not have to grow a row per area.
      {
        key: "aiSettings",
        href: "/admin/settings/ai",
        icon: Sparkles,
        permission: "settings.read",
      },
    ],
  },
  {
    key: "account",
    items: [{ key: "security", href: "/admin/security", icon: ShieldCheck }],
  },
];

/** Flattens the sections, which the command palette searches over. */
export const NAVIGATION_ITEMS: NavigationItem[] = NAVIGATION.flatMap((section) => section.items);

/**
 * Resolves the breadcrumb trail for a pathname.
 *
 * `/admin` is always the root, and the deepest matching entry is the leaf, so a
 * nested detail route still shows where it sits.
 */
export function breadcrumbTrail(pathname: string): NavigationItem[] {
  if (pathname === "/admin") {
    return [];
  }
  const match = NAVIGATION_ITEMS.filter(
    (item) => item.href !== "/admin" && pathname.startsWith(item.href),
  ).sort((left, right) => right.href.length - left.href.length)[0];
  return match ? [match] : [];
}
