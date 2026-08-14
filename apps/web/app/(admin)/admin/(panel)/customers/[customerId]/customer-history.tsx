"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { Listing, Page } from "@/lib/operations";

type ReferralSummary = {
  code?: string;
  codeIssuedAt?: string;
  invitedBy?: string;
  invitedVia?: string;
  invitedAt?: string;
  invitees: { customerId: string; status: string; converted: boolean; invitedAt: string }[];
};

type SupportTicket = {
  id: string;
  subject: string;
  status: string;
  priority: string;
  lastMessageAt: string;
};

type ConsentRecord = {
  kind: string;
  granted: boolean;
  recordedAt: string;
  source?: string;
};

type AuditEvent = {
  id: string;
  action: string;
  actorEmail?: string;
  reason?: string;
  occurredAt: string;
};

/**
 * Who this customer invited, and who invited them.
 *
 * Invitees are shown by identifier and conversion state only. A support
 * operator answering a question about a referrer has no business reading the
 * account details of the people that referrer invited, and the endpoint does
 * not return them.
 */
export function ReferralPanel({ active, base }: { active: boolean; base: string }) {
  const translate = useTranslations("admin.customers.referrals");
  const locale = useTranslations("admin.customers");
  const { data, isLoading } = useSWR<ReferralSummary, ApiError>(
    active ? `${base}/referrals` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const summary = data;
  if (!summary) {
    return null;
  }

  return (
    <Card className="flex flex-col gap-4 p-4">
      <div className="flex flex-wrap gap-x-8 gap-y-3">
        <Fact label={translate("code")} value={summary.code ?? "—"} />
        <Fact
          label={translate("invitedBy")}
          value={
            summary.invitedBy ? (
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/customers/${summary.invitedBy}`}
              >
                {summary.invitedBy.slice(0, 8)}
              </Link>
            ) : (
              "—"
            )
          }
        />
        <Fact label={translate("invited")} value={String(summary.invitees.length)} />
        <Fact
          label={translate("converted")}
          value={String(summary.invitees.filter((invitee) => invitee.converted).length)}
        />
      </div>

      {summary.invitees.length === 0 ? (
        <StateNotice title={translate("noInvitees")} variant="empty" />
      ) : (
        <div className="flex flex-col gap-2">
          {summary.invitees.map((invitee) => (
            <div
              className="flex flex-wrap items-center justify-between gap-3 text-sm"
              key={invitee.customerId}
            >
              <Link
                className="font-mono text-[11px] underline-offset-2 hover:underline"
                href={`/admin/customers/${invitee.customerId}`}
              >
                {invitee.customerId.slice(0, 8)}
              </Link>
              <span className="flex items-center gap-2">
                <Badge variant={invitee.converted ? "success" : "neutral"}>
                  {translate(invitee.converted ? "convertedYes" : "convertedNo")}
                </Badge>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(invitee.invitedAt).toLocaleDateString()}
                </span>
              </span>
            </div>
          ))}
        </div>
      )}
      <p className="text-muted-foreground text-xs">{locale("referrals.privacy")}</p>
    </Card>
  );
}

/** Support tickets and recorded consents, side by side. */
export function SupportPanel({ active, base }: { active: boolean; base: string }) {
  const translate = useTranslations("admin.customers.support");
  const { data: tickets, isLoading } = useSWR<Listing<SupportTicket>, ApiError>(
    active ? `${base}/tickets` : null,
    fetcher,
  );
  const { data: consents } = useSWR<Listing<ConsentRecord>, ApiError>(
    active ? `${base}/consents` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const ticketItems = tickets?.items ?? [];
  const consentItems = consents?.items ?? [];

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card className="flex flex-col gap-2 p-4">
        <h3 className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
          {translate("tickets")}
        </h3>
        {ticketItems.length === 0 ? (
          <StateNotice title={translate("noTickets")} variant="empty" />
        ) : (
          ticketItems.map((ticket) => (
            <div className="flex items-baseline justify-between gap-3 text-sm" key={ticket.id}>
              <span className="truncate">{ticket.subject || translate("noSubject")}</span>
              <span className="flex shrink-0 items-center gap-2">
                <Badge variant={ticket.status === "open" ? "warning" : "neutral"}>
                  {ticket.status}
                </Badge>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(ticket.lastMessageAt).toLocaleDateString()}
                </span>
              </span>
            </div>
          ))
        )}
      </Card>

      <Card className="flex flex-col gap-2 p-4">
        <h3 className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
          {translate("consents")}
        </h3>
        {consentItems.length === 0 ? (
          <StateNotice title={translate("noConsents")} variant="empty" />
        ) : (
          consentItems.map((consent) => (
            <div
              className="flex items-baseline justify-between gap-3 text-sm"
              key={`${consent.kind}-${consent.recordedAt}`}
            >
              <span>{translate(`consentKind.${consent.kind}`)}</span>
              <span className="flex shrink-0 items-center gap-2">
                <Badge variant={consent.granted ? "success" : "neutral"}>
                  {translate(consent.granted ? "granted" : "withdrawn")}
                </Badge>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(consent.recordedAt).toLocaleDateString()}
                </span>
              </span>
            </div>
          ))
        )}
      </Card>
    </div>
  );
}

/**
 * Everything an operator has done to this customer.
 *
 * It reads the same audit trail the governance screen does, filtered to this
 * subject. That is deliberate: a second, customer-scoped log would be a second
 * thing to keep consistent, and the value of an audit trail is that there is
 * exactly one of it.
 */
export function AuditTimeline({
  active,
  customerId,
  locale,
}: {
  active: boolean;
  customerId: string;
  locale: string;
}) {
  const translate = useTranslations("admin.customers.timeline");
  const { data, isLoading } = useSWR<Page<AuditEvent>, ApiError>(
    active ? `/v1/panel/audit?targetType=customer&targetId=${customerId}&pageSize=50` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("emptyDescription")}
        title={translate("empty")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="flex flex-col gap-3 p-4">
      <ol className="flex flex-col gap-2 border-border border-l pl-4">
        {items.map((event) => (
          <li className="flex flex-col gap-0.5" key={event.id}>
            <span className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="font-medium text-sm">{event.action}</span>
              <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                {new Date(event.occurredAt).toLocaleString(locale)}
              </span>
            </span>
            <span className="text-muted-foreground text-xs">
              {event.actorEmail ?? translate("systemActor")}
              {/* The reason is the part that makes an audit line answerable.
                  "Suspended" is a fact; "suspended because of a chargeback" is
                  an answer to the customer asking why. */}
              {event.reason ? ` — ${event.reason}` : ""}
            </span>
          </li>
        ))}
      </ol>
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <span className="flex flex-col">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </span>
      <span className="font-medium text-sm">{value}</span>
    </span>
  );
}
