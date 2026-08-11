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

export type SupportMessage = {
  id: number;
  sender: string;
  body: string;
  authorName?: string;
  delivered: boolean;
  createdAt: string;
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
  definitions: Record<string, string>;
};
