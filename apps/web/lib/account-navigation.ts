import {
  Gift,
  Layers,
  LifeBuoy,
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

/**
 * The customer panel's navigation.
 *
 * Every entry plays an animation from /lottie/account/<key>.lottie on hover
 * and tap; lib/sidebar-animations.test.ts fails when one is missing.
 */
export type AccountNavEntry = { href: string; icon: LucideIcon; key: string };

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
export const TABS: AccountNavEntry[] = [
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
export const SECONDARY: AccountNavEntry[] = [
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
 * Entries the sidebar shows only when they apply — the Stars product, the
 * smart subscription — linked by the shell itself. Listed so their
 * animations are checked like the rest.
 */
export const CONDITIONAL_ENTRY_KEYS = ["stars", "servers"] as const;

/** Every animation the customer panel's navigation plays. */
export const ACCOUNT_ANIMATION_KEYS = [
  ...TABS.map((tab) => tab.key),
  ...SECONDARY.map((item) => item.key),
  ...CONDITIONAL_ENTRY_KEYS,
];
