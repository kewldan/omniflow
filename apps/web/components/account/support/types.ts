/**
 * The wire shapes of `/v1/account/support`, `/v1/account/news`, and
 * `/v1/account/preferences`.
 *
 * They are written by hand against `internal/httpapi/accountsupport.go` rather
 * than imported from the generated client, for the same reason the rest of the
 * customer panel does it: Orval's SWR generator emits its own transport, so the
 * panel calls the shared `apiFetch` and keeps the shapes beside the screens that
 * read them. Anything the server computes — `open`, `canReply`, `downloadable`,
 * `unread` — is consumed as given and never re-derived here, because a rule
 * living in two places is a rule that eventually disagrees with itself.
 */

/** The publishable half of the installation's attachment and ticket limits. */
export type SupportLimits = {
  maxAttachmentBytes: number;
  allowedMediaTypes: string[];
  maxOpenTickets: number;
  maxMessageLength: number;
  maxSubjectLength: number;
  /** Files one conversation may carry from the web. */
  maxAttachmentsPerTicket: number;
};

export type TicketStatus = "open" | "pending" | "resolved" | "closed" | "merged";

export type MessageAuthor = "customer" | "operator" | "system";

export type SupportTicket = {
  id: string;
  subject: string;
  status: TicketStatus;
  priority: string;
  /** Counts against the open-conversation quota. Resolved tickets do not. */
  open: boolean;
  /** The composer is enabled from this and from nothing else. */
  canReply: boolean;
  unreadCount: number;
  messageCount: number;
  createdAt: string;
  updatedAt: string;
  lastMessageAt: string;
  /** Present only when an operator folded this conversation into another. */
  mergedIntoTicketId?: string;
};

export type SupportAttachment = {
  id: string;
  kind: "photo" | "document";
  fileName: string;
  mediaType: string;
  sizeBytes: number;
  /**
   * False for a file that arrived through Telegram: Omniflow stored the
   * reference rather than the bytes, so there is nothing here to serve.
   */
  downloadable: boolean;
  createdAt: string;
};

export type SupportMessage = {
  id: number;
  author: MessageAuthor;
  body: string;
  /**
   * Unread on every surface, not just this one. It is a property of the message
   * record the bot and the panel share.
   */
  unread: boolean;
  createdAt: string;
  attachments: SupportAttachment[];
};

export type SupportConversation = { ticket: SupportTicket; messages: SupportMessage[] };

export type SupportTicketPage = { items: SupportTicket[]; nextCursor?: string };

export type NewsCategory = "news" | "announcement" | "incident" | "maintenance";

export type NewsItem = {
  id: string;
  slug: string;
  category: NewsCategory;
  /** 'transactional' for a service notice, 'marketing' for one that needed consent. */
  class: "transactional" | "marketing";
  title: string;
  body: string;
  read: boolean;
  publishedAt: string;
};

export type NewsPage = {
  items: NewsItem[];
  nextCursor?: string;
  unreadCount: number;
  /** The locale the posts were resolved in, which is not always the one asked for. */
  locale: "ru" | "en";
};

export type ContactChannel = {
  id: string;
  kind: "email" | "phone" | "telegram";
  verified: boolean;
  transactional: boolean;
  marketing: boolean;
  createdAt: string;
  /**
   * There is deliberately no address field. The API never returns one, and the
   * screen must not invent a masked placeholder that looks like it did.
   */
};

export type CommunicationPreferences = {
  locale: "auto" | "ru" | "en";
  notifications: { expiry: boolean; traffic: boolean; renewal: boolean; news: boolean };
  /** Absent when the customer has set no window. */
  quietHours?: { startHour: number; endHour: number };
  marketing: {
    enabled: boolean;
    decidedAt?: string;
    source?: string;
    policyVersion?: string;
  };
  contacts: ContactChannel[];
  /** Present while every non-essential message is being held back. */
  suppression?: { reason: string; createdAt: string };
};

/**
 * A partial change to the preferences.
 *
 * Every field is optional because the route is a genuine PATCH: an omitted field
 * is left alone. A screen that renders one switch must send that one switch, not
 * the whole document it happens to be holding — otherwise a stale copy of a
 * value the customer changed elsewhere would be written back over the current
 * one.
 */
export type PreferencesPatch = {
  locale?: "auto" | "ru" | "en";
  notifications?: Partial<CommunicationPreferences["notifications"]>;
  marketing?: boolean;
  quietHours?: { startHour: number; endHour: number };
};

/**
 * One notification, as its recipient sees it.
 *
 * `reason` is the field worth reading. A status other than `sent` carries one,
 * and the codes an installation produces on purpose — `quiet_hours`,
 * `frequency_cap`, `no_consent` — say that a setting held the message back,
 * which is something the customer can change. There is no body: the record says
 * that a notice of a kind happened, not what it said.
 */
export type NotificationDelivery = {
  kind: string;
  status: "pending" | "deferred" | "sent" | "failed" | "suppressed";
  reason?: string;
  scheduledAt: string;
  sentAt?: string;
  deferredUntil?: string;
  subscriptionSlot?: number;
  subscriptionLabel?: string;
};

export const SUPPORT_TICKETS_KEY = "/v1/account/support/tickets?limit=20";
export const SUPPORT_LIMITS_KEY = "/v1/account/support/limits";
export const NEWS_KEY = "/v1/account/news?limit=20";
export const PREFERENCES_KEY = "/v1/account/preferences";
export const NOTIFICATION_HISTORY_KEY = "/v1/account/notifications?limit=30";
