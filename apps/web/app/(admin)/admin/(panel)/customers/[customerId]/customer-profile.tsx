"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type BlocklistMatch,
  type CustomerProfile,
  formatBytes,
  formatMoney,
  type LedgerLine,
  type Listing,
  type OrderSummary,
  type SubscriptionDetail,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

import { AuditTimeline, ReferralPanel, SupportPanel } from "./customer-history";
import { SubscriptionActions, SubscriptionDevices } from "./subscription-actions";

/**
 * One customer, with every surface that touches them.
 *
 * The tabs are loaded lazily by SWR keying on the active tab, so opening a
 * profile does not fetch a wallet ledger nobody asked for — and a support
 * operator who cannot read finance never triggers the request at all.
 */
export function CustomerProfileView({ customerId }: { customerId: string }) {
  const translate = useTranslations("admin.customers");
  const locale = useLocale();
  const { can } = useSession();
  const [tab, setTab] = useState("subscriptions");

  const base = `/v1/panel/customers/${customerId}`;
  const {
    data: profile,
    error,
    isLoading,
    mutate,
  } = useSWR<CustomerProfile, ApiError>(base, fetcher);

  if (error) {
    return (
      <StateNotice
        description={translate("notFoundDescription")}
        title={error.status === 404 ? translate("notFound") : translate("loadFailed")}
        variant={error.status === 404 ? "empty" : "danger"}
      />
    );
  }

  if (isLoading || !profile) {
    return (
      <div className="flex flex-col gap-4">
        <Skeleton className="h-10 w-72" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        actions={
          can("customers.write") ? (
            <StatusAction customerId={customerId} onDone={() => mutate()} status={profile.status} />
          ) : undefined
        }
        eyebrow={translate("title")}
        title={<span className="font-mono text-xl">{profile.id}</span>}
      />

      <Card>
        <CardHeader>
          <CardTitle>{translate("overview")}</CardTitle>
          <CardDescription>
            {translate("createdAt", { at: new Date(profile.createdAt).toLocaleString(locale) })}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-x-8 gap-y-3">
          <Fact label={translate("columns.status")} value={translate(`status.${profile.status}`)} />
          <Fact label={translate("columns.telegram")} value={String(profile.telegramId ?? "—")} />
          <Fact label={translate("columns.locale")} value={profile.locale} />
          <Fact
            label={translate("facts.subscriptions")}
            value={String(profile.activeSubscriptions)}
          />
          <Fact label={translate("facts.orders")} value={String(profile.orderCount)} />
          <Fact label={translate("facts.tickets")} value={String(profile.openTickets)} />
          <Fact label={translate("facts.referrals")} value={String(profile.referralCount)} />
          {profile.openFlags > 0 && (
            <span className="flex items-center gap-2">
              <Badge variant="warning">
                {translate("facts.flags", { count: profile.openFlags })}
              </Badge>
            </span>
          )}
          {profile.allowlisted && <Badge variant="neutral">{translate("facts.allowlisted")}</Badge>}
        </CardContent>
      </Card>

      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="subscriptions">{translate("tabs.subscriptions")}</TabsTrigger>
          <TabsTrigger value="orders">{translate("tabs.orders")}</TabsTrigger>
          {can("finance.read") && (
            <TabsTrigger value="wallet">{translate("tabs.wallet")}</TabsTrigger>
          )}
          <TabsTrigger value="referrals">{translate("tabs.referrals")}</TabsTrigger>
          {can("support.read") && (
            <TabsTrigger value="support">{translate("tabs.support")}</TabsTrigger>
          )}
          {can("risk.read") && <TabsTrigger value="risk">{translate("tabs.risk")}</TabsTrigger>}
          {can("audit.read") && (
            <TabsTrigger value="timeline">{translate("tabs.timeline")}</TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="subscriptions">
          <SubscriptionList active={tab === "subscriptions"} base={base} customerId={customerId} />
        </TabsContent>
        <TabsContent value="orders">
          <OrderList active={tab === "orders"} base={base} locale={locale} />
        </TabsContent>
        {can("finance.read") && (
          <TabsContent value="wallet">
            <WalletList active={tab === "wallet"} base={base} locale={locale} />
          </TabsContent>
        )}
        <TabsContent value="referrals">
          <ReferralPanel active={tab === "referrals"} base={base} />
        </TabsContent>
        {can("support.read") && (
          <TabsContent value="support">
            <SupportPanel active={tab === "support"} base={base} />
          </TabsContent>
        )}
        {can("risk.read") && (
          <TabsContent value="risk">
            <RiskList active={tab === "risk"} base={base} locale={locale} />
          </TabsContent>
        )}
        {can("audit.read") && (
          <TabsContent value="timeline">
            <AuditTimeline active={tab === "timeline"} customerId={customerId} locale={locale} />
          </TabsContent>
        )}
      </Tabs>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex flex-col">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </span>
      <span className="font-medium tabular-nums">{value}</span>
    </span>
  );
}

/**
 * Suspend or reactivate, with a mandatory reason.
 *
 * The API refuses a status change with no reason, so the button stays disabled
 * until one is typed rather than letting an operator discover the requirement
 * from an error. Suspension does not itself disable a Remnawave user: the
 * fulfillment pipeline observes customer state, and a panel click that bypassed
 * it would leave the two disagreeing.
 */
function StatusAction({
  customerId,
  onDone,
  status,
}: {
  customerId: string;
  onDone: () => void;
  status: string;
}) {
  const translate = useTranslations("admin.customers");
  const reasonId = useId();
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const next = status === "suspended" ? "active" : "suspended";

  return (
    <Card className="w-full max-w-sm p-3">
      <div className="flex flex-col gap-2">
        <Label htmlFor={reasonId}>{translate("actions.reason")}</Label>
        <Input
          id={reasonId}
          onChange={(event) => setReason(event.target.value)}
          placeholder={translate("actions.reasonPlaceholder")}
          value={reason}
        />
        <Button
          disabled={pending || reason.trim().length === 0}
          onClick={async () => {
            const ok = await run(`/v1/panel/customers/${customerId}/status`, {
              body: { status: next },
              method: "POST",
              reason: reason.trim(),
            });
            if (ok) {
              setReason("");
              onDone();
            }
          }}
          size="sm"
          variant={next === "suspended" ? "destructive" : "outline"}
        >
          {translate(`actions.${next === "suspended" ? "suspend" : "reactivate"}`)}
        </Button>
        {error && <p className="text-danger-foreground text-xs">{error.message}</p>}
      </div>
    </Card>
  );
}

function SubscriptionList({
  active,
  base,
  customerId,
}: {
  active: boolean;
  base: string;
  customerId: string;
}) {
  const translate = useTranslations("admin.customers");
  const { data, isLoading } = useSWR<Listing<SubscriptionDetail>, ApiError>(
    active ? `${base}/subscriptions` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <StateNotice title={translate("empty.subscriptions")} variant="empty" />;
  }

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      {items.map((subscription) => (
        <Card className="p-4" key={subscription.id}>
          <div className="flex items-start justify-between gap-3">
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">{subscription.label}</span>
              <span className="font-mono text-[11px] text-muted-foreground">
                {translate("subscription.slot", { slot: subscription.slot })}
                {subscription.remnawaveUsername ? ` · ${subscription.remnawaveUsername}` : ""}
              </span>
            </div>
            <Badge variant={subscription.status === "active" ? "success" : "neutral"}>
              {subscription.status}
            </Badge>
          </div>
          <dl className="mt-3 grid grid-cols-2 gap-2 text-sm">
            <Detail label={translate("subscription.plan")} value={subscription.planCode ?? "—"} />
            <Detail
              label={translate("subscription.entitlement")}
              value={subscription.entitlementStatus || "—"}
            />
            <Detail
              label={translate("subscription.endsAt")}
              value={subscription.endsAt ? new Date(subscription.endsAt).toLocaleDateString() : "—"}
            />
            <Detail
              label={translate("subscription.traffic")}
              value={
                subscription.trafficAllowanceBytes
                  ? formatBytes(subscription.trafficAllowanceBytes)
                  : translate("subscription.unlimited")
              }
            />
          </dl>

          <SubscriptionDevices customerId={customerId} subscriptionId={subscription.id} />
          <SubscriptionActions
            customerId={customerId}
            hasEntitlement={Boolean(subscription.entitlementId)}
            subscriptionId={subscription.id}
          />
        </Card>
      ))}
    </div>
  );
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </dt>
      <dd className="tabular-nums">{value}</dd>
    </div>
  );
}

function OrderList({ active, base, locale }: { active: boolean; base: string; locale: string }) {
  const translate = useTranslations("admin.customers");
  const { data, isLoading } = useSWR<Listing<OrderSummary>, ApiError>(
    active ? `${base}/orders` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <StateNotice title={translate("empty.orders")} variant="empty" />;
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("order.createdAt")}</TableHead>
            <TableHead>{translate("order.operation")}</TableHead>
            <TableHead>{translate("order.state")}</TableHead>
            <TableHead>{translate("order.paid")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((order) => (
            <TableRow key={order.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(order.createdAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{order.operation}</TableCell>
              <TableCell>
                <Badge variant="neutral">{order.state}</Badge>
              </TableCell>
              <TableCell data-numeric>
                {formatMoney(order.paidMinor, order.currency, locale)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function WalletList({ active, base, locale }: { active: boolean; base: string; locale: string }) {
  const translate = useTranslations("admin.customers");
  const { data, isLoading } = useSWR<Listing<LedgerLine>, ApiError>(
    active ? `${base}/wallet` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <StateNotice title={translate("empty.wallet")} variant="empty" />;
  }

  return (
    <Card className="overflow-x-auto">
      {/* The ledger is append-only: there is no edit control here by design. A
          correction is a compensating entry, made through the finance surface. */}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("wallet.createdAt")}</TableHead>
            <TableHead>{translate("wallet.type")}</TableHead>
            <TableHead>{translate("wallet.reference")}</TableHead>
            <TableHead>{translate("wallet.amount")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((line) => (
            <TableRow key={line.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(line.createdAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{line.type}</TableCell>
              <TableCell className="max-w-56 truncate font-mono text-[11px] text-muted-foreground">
                {line.referenceType}:{line.referenceId}
              </TableCell>
              <TableCell data-numeric>
                <span className={line.amountMinor < 0 ? "text-danger-foreground" : undefined}>
                  {formatMoney(line.amountMinor, line.currency, locale)}
                </span>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function RiskList({ active, base, locale }: { active: boolean; base: string; locale: string }) {
  const translate = useTranslations("admin.customers");
  const { data, isLoading } = useSWR<Listing<BlocklistMatch>, ApiError>(
    active ? `${base}/risk` : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.riskDescription")}
        title={translate("empty.risk")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("risk.detectedAt")}</TableHead>
            <TableHead>{translate("risk.source")}</TableHead>
            <TableHead>{translate("risk.subject")}</TableHead>
            <TableHead>{translate("risk.status")}</TableHead>
            <TableHead>{translate("risk.reason")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((match) => (
            <TableRow key={match.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(match.detectedAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{match.sourceName}</TableCell>
              <TableCell className="text-muted-foreground">{match.subjectKind}</TableCell>
              <TableCell>
                <Badge variant={match.status === "blocked" ? "danger" : "neutral"}>
                  {match.status}
                </Badge>
              </TableCell>
              <TableCell className="max-w-64 truncate text-muted-foreground">
                {match.decisionReason ?? "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
