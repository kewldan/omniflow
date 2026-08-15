"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { ArrowRight, Download, RotateCcw } from "lucide-react";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader, ResourceTable } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import {
  formatDuration,
  formatMoney,
  type Listing,
  type OrderSummary,
  type Page,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";
import { usePreferences } from "@/lib/use-preferences";
import { useUrlFilters } from "@/lib/use-url-filters";

const STATES = [
  "draft",
  "pending",
  "paid",
  "fulfilled",
  "cancelled",
  "expired",
  "partially_refunded",
  "refunded",
] as const;

const OPERATIONS = [
  "purchase",
  "upgrade",
  "downgrade",
  "extension",
  "topup",
  "addon",
  "gift",
  "goods",
] as const;

type StuckPayment = {
  id: string;
  orderId: string;
  customerId: string;
  operation: string;
  provider: string;
  status: string;
  amountMinor: number;
  currency: string;
  updatedAt: string;
};

type DunningAttempt = {
  id: string;
  customerId: string;
  cycleKey: string;
  attempt: number;
  funding: string;
  outcome: string;
  failureCode?: string;
  scheduledFor: string;
  createdAt: string;
};

/**
 * Finance: order search, the reconciliation queue, and the failed-charge review.
 *
 * The three are tabs of one page rather than three routes because they are the
 * same question at different stages — money expected, money stuck, money that
 * did not arrive — and an operator moves between them constantly.
 */
export function FinanceBrowser() {
  const translate = useTranslations("admin.finance");
  const [tab, setTab] = useState("orders");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="orders">{translate("tabs.orders")}</TabsTrigger>
          <TabsTrigger value="stuck">{translate("tabs.stuck")}</TabsTrigger>
          <TabsTrigger value="charges">{translate("tabs.charges")}</TabsTrigger>
        </TabsList>
        <TabsContent value="orders">
          <OrderSearch />
        </TabsContent>
        <TabsContent value="stuck">
          <StuckPayments active={tab === "stuck"} />
        </TabsContent>
        <TabsContent value="charges">
          <FailedCharges active={tab === "charges"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function OrderSearch() {
  const translate = useTranslations("admin.finance");
  const locale = useLocale();
  const stateId = useId();
  const operationId = useId();

  const { filters, cursorStack, setFilter, reset, goNext, goPrevious, hasPrevious } = useUrlFilters(
    ["state", "operation"],
  );
  const { preferences } = usePreferences();
  const pageSize = preferences.pageSize || 25;

  const query = toQuery({
    cursor: filters.cursor,
    operation: filters.operation,
    pageSize,
    state: filters.state,
  });

  const { data, error, isLoading, isValidating, mutate } = useSWR<Page<OrderSummary>, ApiError>(
    `/v1/panel/finance/orders${query}`,
    fetcher,
    { keepPreviousData: true },
  );

  const filtersActive = Boolean(filters.state || filters.operation);

  return (
    <div className="flex flex-col gap-4">
      <Card className="p-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-40 flex-col gap-1.5">
            <Label htmlFor={stateId}>{translate("filters.state")}</Label>
            <Select
              onValueChange={(value) => setFilter("state", value === "all" ? "" : value)}
              value={filters.state || "all"}
            >
              <SelectTrigger id={stateId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {STATES.map((state) => (
                  <SelectItem key={state} value={state}>
                    {state}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex min-w-40 flex-col gap-1.5">
            <Label htmlFor={operationId}>{translate("filters.operation")}</Label>
            <Select
              onValueChange={(value) => setFilter("operation", value === "all" ? "" : value)}
              value={filters.operation || "all"}
            >
              <SelectTrigger id={operationId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {OPERATIONS.map((operation) => (
                  <SelectItem key={operation} value={operation}>
                    {operation}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {filtersActive && (
            <Button onClick={reset} size="sm" variant="ghost">
              <RotateCcw />
              {translate("filters.clear")}
            </Button>
          )}

          <Button asChild className="ml-auto" size="sm" variant="outline">
            {/* A plain link: the response is a streamed CSV attachment, so the
                browser's own download handling is the correct one. */}
            <a href={`/v1/panel/finance/export${query}`}>
              <Download />
              {translate("export")}
            </a>
          </Button>
        </div>
      </Card>

      <ResourceTable<OrderSummary>
        columns={[
          translate("columns.createdAt"),
          translate("columns.customer"),
          translate("columns.operation"),
          translate("columns.state"),
          translate("columns.paid"),
          translate("columns.refunded"),
          "",
        ]}
        error={error}
        filtersActive={filtersActive}
        loading={isLoading}
        onClearFilters={reset}
        onRetry={() => mutate()}
        pagination={{
          hasNext: Boolean(data?.nextCursor),
          hasPrevious,
          onNext: () => goNext(data?.nextCursor),
          onPrevious: goPrevious,
          page: cursorStack.length + 1,
        }}
        renderRow={(order) => (
          <TableRow key={order.id}>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/finance/${order.id}`}
              >
                {new Date(order.createdAt).toLocaleString(locale)}
              </Link>
            </TableCell>
            <TableCell className="font-mono text-[11px]">
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/customers/${order.customerId}`}
              >
                {order.customerId.slice(0, 8)}
              </Link>
            </TableCell>
            <TableCell>{order.operation}</TableCell>
            <TableCell>
              <Badge
                variant={
                  order.state === "fulfilled" || order.state === "paid" ? "success" : "neutral"
                }
              >
                {order.state}
              </Badge>
            </TableCell>
            <TableCell data-numeric>
              {formatMoney(order.paidMinor, order.currency, locale)}
            </TableCell>
            <TableCell data-numeric>
              {order.refundedMinor > 0
                ? formatMoney(order.refundedMinor, order.currency, locale)
                : "—"}
            </TableCell>
            {/* The order detail was only ever reachable by pressing the
                timestamp, which is not where anybody looks for a way in. The
                arrow says the row opens; the timestamp link stays because it
                still works and removing it would break a habit for no gain. */}
            <TableCell className="w-10 text-right">
              <Button asChild size="icon-sm" variant="ghost">
                <Link aria-label={translate("openOrder")} href={`/admin/finance/${order.id}`}>
                  <ArrowRight aria-hidden />
                </Link>
              </Button>
            </TableCell>
          </TableRow>
        )}
        rows={data?.items ?? undefined}
        validating={isValidating}
      />
    </div>
  );
}

/**
 * The reconciliation queue.
 *
 * A payment that has been in flight for hours is a customer who has paid and is
 * waiting. Reconciling re-polls the provider through the existing idempotent
 * payment service; the panel never decides how a payment settles.
 */
function StuckPayments({ active }: { active: boolean }) {
  const translate = useTranslations("admin.finance");
  const locale = useLocale();
  const { can } = useSession();
  const { data, isLoading, mutate } = useSWR<
    Listing<StuckPayment & { ageSeconds?: number }>,
    ApiError
  >(active ? "/v1/panel/finance/stuck-payments" : null, fetcher);

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.stuckDescription")}
        title={translate("empty.stuck")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.updatedAt")}</TableHead>
            <TableHead>{translate("columns.provider")}</TableHead>
            <TableHead>{translate("columns.state")}</TableHead>
            <TableHead>{translate("columns.amount")}</TableHead>
            <TableHead>{translate("columns.customer")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((payment) => (
            <TableRow key={payment.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(payment.updatedAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{payment.provider}</TableCell>
              <TableCell>
                <Badge variant="warning">{payment.status}</Badge>
              </TableCell>
              <TableCell data-numeric>
                {formatMoney(payment.amountMinor, payment.currency, locale)}
              </TableCell>
              <TableCell className="font-mono text-[11px]">
                <Link
                  className="underline-offset-2 hover:underline"
                  href={`/admin/customers/${payment.customerId}`}
                >
                  {payment.customerId.slice(0, 8)}
                </Link>
              </TableCell>
              <TableCell>
                {can("finance.write") && (
                  <ReconcilePayment intentId={payment.id} onDone={() => mutate()} />
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

/**
 * Asks the provider what actually happened to one payment.
 *
 * This is the safe half of a retry. It never charges anything and never
 * rewrites a status by hand: it re-polls the provider and lets the ordinary
 * settlement path record the answer, so a payment that quietly succeeded is
 * reconciled and one that quietly failed stops looking stuck. A panel that
 * could set a payment to "succeeded" directly would be a way to grant an
 * entitlement nobody paid for.
 */
function ReconcilePayment({ intentId, onDone }: { intentId: string; onDone: () => void }) {
  const translate = useTranslations("admin.finance");
  const { run, pending } = useOperatorAction();
  return (
    <Button
      disabled={pending}
      onClick={async () => {
        const ok = await run(`/v1/panel/finance/payments/${intentId}/reconcile`, {
          body: { outcome: "requested" },
          method: "POST",
        });
        if (ok) {
          onDone();
        }
      }}
      size="sm"
      variant="outline"
    >
      {translate("detail.reconcile")}
    </Button>
  );
}

/**
 * Failed automatic charges.
 *
 * This is a review surface, not a control surface. A failed charge retries on
 * its own bounded schedule and then hands the customer back to manual renewal;
 * nothing here re-triggers one, because the customer has already been told to
 * renew and charging them again from a panel click would be a second attempt
 * they did not ask for.
 */
function FailedCharges({ active }: { active: boolean }) {
  const translate = useTranslations("admin.finance");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Page<DunningAttempt>, ApiError>(
    active ? "/v1/panel/finance/failed-charges" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.chargesDescription")}
        title={translate("empty.charges")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.createdAt")}</TableHead>
            <TableHead>{translate("columns.customer")}</TableHead>
            <TableHead>{translate("columns.attempt")}</TableHead>
            <TableHead>{translate("columns.funding")}</TableHead>
            <TableHead>{translate("columns.outcome")}</TableHead>
            <TableHead>{translate("columns.failureCode")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((attempt) => (
            <TableRow key={attempt.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(attempt.createdAt).toLocaleString(locale)}
              </TableCell>
              <TableCell className="font-mono text-[11px]">
                <Link
                  className="underline-offset-2 hover:underline"
                  href={`/admin/customers/${attempt.customerId}`}
                >
                  {attempt.customerId.slice(0, 8)}
                </Link>
              </TableCell>
              <TableCell data-numeric>{attempt.attempt}</TableCell>
              <TableCell>{translate(`funding.${attempt.funding}`)}</TableCell>
              <TableCell>
                <Badge variant={attempt.outcome === "abandoned" ? "danger" : "warning"}>
                  {translate(`outcome.${attempt.outcome}`)}
                </Badge>
              </TableCell>
              <TableCell className="font-mono text-[11px] text-muted-foreground">
                {attempt.failureCode ?? "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <p className="border-border border-t p-3 text-muted-foreground text-xs">
        {translate("chargesNote", { window: formatDuration(60 * 60 * 40) })}
      </p>
    </Card>
  );
}
