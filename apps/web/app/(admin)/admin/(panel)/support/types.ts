/**
 * The support desk's shapes.
 *
 * Messages and notes are separate types reading separate lists, mirroring the
 * server. Merging them into one array with a `visibility` field would be
 * convenient here and would put one bad render away from showing a customer an
 * operator's private note.
 */
export type SupportQueue = {
  id: string;
  code: string;
  nameEn: string;
  nameRu: string;
  firstResponseTargetSeconds: number;
  resolutionTargetSeconds: number;
  isDefault: boolean;
  sortOrder: number;
  openCount: number;
  unassignedCount: number;
  breachedCount: number;
};

export type SupportTicket = {
  id: string;
  customerId: string;
  queueId: string;
  queueCode: string;
  subject: string;
  status: string;
  priority: string;
  assigneeId?: string;
  assigneeName?: string;
  tags: string[];
  messageCount: number;
  unreadCount: number;
  reopenedCount: number;
  firstResponseBreached: boolean;
  mergedIntoTicketId?: string;
  createdAt: string;
  lastMessageAt: string;
  firstResponseAt?: string;
  resolvedAt?: string;
};

/**
 * The push outcome for an operator or system message. Absent on a customer
 * message, which is never pushed. `undeliverable` is the one worth reading: the
 * customer will only see this in the web panel, and `queued` used to be shown
 * forever in its place.
 */
export type DeliveryState = "queued" | "retrying" | "delivered" | "undeliverable" | "failed";

/**
 * One file on a conversation. `origin` says where the bytes live: `web` for an
 * upload this installation holds and can serve, `telegram` for a reference to a
 * file in the customer's chat, which the panel describes but never fetches.
 */
export type SupportAttachment = {
  id: string;
  messageId: number;
  kind: "photo" | "document";
  fileName: string;
  mediaType: string;
  sizeBytes: number;
  origin: "web" | "telegram";
  downloadable: boolean;
  createdAt: string;
};

export type SupportMessage = {
  id: number;
  sender: string;
  body: string;
  authorName?: string;
  delivered: boolean;
  delivery?: DeliveryState;
  /** The classified code behind an undeliverable, failed, or retrying push. */
  deliveryReason?: string;
  createdAt: string;
  attachments: SupportAttachment[];
};

export type SupportNote = {
  id: number;
  authorName: string;
  body: string;
  createdAt: string;
};

export type TicketDetail = {
  ticket: SupportTicket;
  messages: SupportMessage[];
  notes: SupportNote[];
};

export type CannedResponse = {
  id: string;
  code: string;
  titleEn: string;
  titleRu: string;
  bodyEn: string;
  bodyRu: string;
  requiresPermission: string;
  usageCount: number;
};

export type SupportTag = {
  id: string;
  code: string;
  nameEn: string;
  nameRu: string;
};

export type OperatorLoad = {
  operatorId: string;
  displayName: string;
  replies: number;
  openTickets: number;
  resolvedTickets: number;
  medianFirstResponseSeconds: number;
};

export type SupportReportData = {
  openTickets: number;
  unassignedTickets: number;
  breachedTickets: number;
  resolvedInWindow: number;
  medianFirstResponseSeconds: number;
  windowSeconds: number;
  operators: OperatorLoad[];
};
