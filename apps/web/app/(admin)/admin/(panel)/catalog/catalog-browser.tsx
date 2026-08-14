"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { Fragment, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  formatMoney,
  type Listing,
  type PlanSummary,
  type Promotion,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

import { Addons } from "./addon-editor";
import { PlanVersionEditor } from "./version-editor";

/**
 * Plans, add-ons, and promotions.
 *
 * The catalogue is versioned, so nothing here rewrites history: hiding a plan
 * stops new purchases and leaves every order that already bought it priced
 * against the immutable version it was sold at. That is why archiving shows the
 * number of order lines rather than refusing when there are any.
 */
export function CatalogBrowser() {
  const translate = useTranslations("admin.catalog");
  const [tab, setTab] = useState("plans");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="plans">{translate("tabs.plans")}</TabsTrigger>
          <TabsTrigger value="addons">{translate("tabs.addons")}</TabsTrigger>
          <TabsTrigger value="promotions">{translate("tabs.promotions")}</TabsTrigger>
        </TabsList>
        <TabsContent value="plans">
          <Plans active={tab === "plans"} />
        </TabsContent>
        <TabsContent value="addons">
          <Addons active={tab === "addons"} />
        </TabsContent>
        <TabsContent value="promotions">
          <Promotions active={tab === "promotions"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Plans({ active }: { active: boolean }) {
  const translate = useTranslations("admin.catalog");
  const { can } = useSession();
  const { run, pending } = useOperatorAction();
  const [editing, setEditing] = useState("");

  const { data, isLoading, mutate } = useSWR<Listing<PlanSummary>, ApiError>(
    active ? "/v1/panel/catalog/plans" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.plansDescription")}
        title={translate("empty.plans")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.code")}</TableHead>
            <TableHead>{translate("columns.kind")}</TableHead>
            <TableHead>{translate("columns.version")}</TableHead>
            <TableHead>{translate("columns.orders")}</TableHead>
            <TableHead>{translate("columns.concurrency")}</TableHead>
            <TableHead>{translate("columns.visible")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((plan) => (
            <Fragment key={plan.id}>
              <TableRow>
                <TableCell className="font-mono text-[12px]">
                  <span className="flex items-center gap-2">
                    {plan.code}
                    {plan.archivedAt && <Badge variant="neutral">{translate("archived")}</Badge>}
                  </span>
                </TableCell>
                <TableCell>{translate(`kind.${plan.kind}`)}</TableCell>
                <TableCell data-numeric>v{plan.latestVersion}</TableCell>
                <TableCell data-numeric>{plan.orderLineCount}</TableCell>
                <TableCell data-numeric>{plan.maxConcurrentPerCustomer ?? "—"}</TableCell>
                <TableCell>
                  <Switch
                    aria-label={translate("columns.visible")}
                    checked={plan.visible}
                    disabled={!can("catalog.write") || pending || Boolean(plan.archivedAt)}
                    onCheckedChange={async (visible) => {
                      const ok = await run(`/v1/panel/catalog/plans/${plan.id}`, {
                        body: {
                          maxConcurrentPerCustomer: plan.maxConcurrentPerCustomer ?? null,
                          sortOrder: plan.sortOrder,
                          visible,
                        },
                        method: "PATCH",
                      });
                      if (ok) {
                        await mutate();
                      }
                    }}
                  />
                </TableCell>
                <TableCell>
                  {can("catalog.write") && !plan.archivedAt && (
                    <Button
                      onClick={() => setEditing(editing === plan.id ? "" : plan.id)}
                      size="sm"
                      variant="ghost"
                    >
                      {translate(editing === plan.id ? "version.close" : "version.open")}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
              {editing === plan.id && (
                <TableRow>
                  <TableCell className="p-2" colSpan={7}>
                    <PlanVersionEditor
                      onPublished={() => {
                        setEditing("");
                        void mutate();
                      }}
                      planId={plan.id}
                    />
                  </TableCell>
                </TableRow>
              )}
            </Fragment>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Promotions({ active }: { active: boolean }) {
  const translate = useTranslations("admin.catalog");
  const locale = useLocale();
  const { can } = useSession();
  const { run, pending } = useOperatorAction();

  const { data, isLoading, mutate } = useSWR<Listing<Promotion>, ApiError>(
    active ? "/v1/panel/catalog/promotions" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <StateNotice title={translate("empty.promotions")} variant="empty" />;
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.code")}</TableHead>
            <TableHead>{translate("columns.reward")}</TableHead>
            <TableHead>{translate("columns.stacking")}</TableHead>
            <TableHead>{translate("columns.redemptions")}</TableHead>
            <TableHead>{translate("columns.discount")}</TableHead>
            <TableHead>{translate("columns.active")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((promotion) => (
            <TableRow key={promotion.id}>
              <TableCell className="font-mono text-[12px]">{promotion.code}</TableCell>
              <TableCell>{describeReward(promotion, locale, translate)}</TableCell>
              <TableCell>
                {/* Stacking is off by default: two promotions combine only when
                    both say they may, and precedence orders the evaluation. */}
                <span className="flex items-center gap-2">
                  <Badge variant={promotion.stackable ? "success" : "neutral"}>
                    {translate(promotion.stackable ? "stackable" : "exclusive")}
                  </Badge>
                  <span className="font-mono text-[11px] text-muted-foreground">
                    #{promotion.precedence}
                  </span>
                </span>
              </TableCell>
              <TableCell data-numeric>
                {promotion.redemptionCount}
                {promotion.redemptionLimit ? ` / ${promotion.redemptionLimit}` : ""}
              </TableCell>
              <TableCell data-numeric>
                {promotion.discountMinor > 0 && promotion.currency
                  ? formatMoney(promotion.discountMinor, promotion.currency, locale)
                  : "—"}
              </TableCell>
              <TableCell>
                <Switch
                  aria-label={translate("columns.active")}
                  checked={promotion.active}
                  disabled={!can("catalog.write") || pending}
                  onCheckedChange={async (nextActive) => {
                    const ok = await run(`/v1/panel/catalog/promotions/${promotion.id}`, {
                      body: {
                        active: nextActive,
                        currency: promotion.currency ?? "",
                        eligibility: {},
                        endsAt: promotion.endsAt ?? null,
                        perCustomerLimit: promotion.perCustomerLimit,
                        precedence: promotion.precedence,
                        redemptionLimit: promotion.redemptionLimit ?? null,
                        stackable: promotion.stackable,
                        startsAt: promotion.startsAt ?? null,
                        value: promotion.value,
                      },
                      method: "PATCH",
                    });
                    if (ok) {
                      await mutate();
                    }
                  }}
                />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

/**
 * Renders a promotion's reward in its own unit.
 *
 * A percentage, a fixed discount, a wallet credit, a number of days, and a
 * granted trial are five different things, and showing all five as a bare
 * integer would make the list unreadable.
 */
function describeReward(
  promotion: Promotion,
  locale: string,
  translate: (key: string, values?: Record<string, string | number>) => string,
): string {
  switch (promotion.kind) {
    case "percent":
      return translate("reward.percent", { value: (promotion.value / 100).toFixed(2) });
    case "fixed":
      return translate("reward.fixed", {
        amount: formatMoney(promotion.value, promotion.currency ?? "", locale),
      });
    case "wallet_credit":
      return translate("reward.credit", {
        amount: formatMoney(promotion.value, promotion.currency ?? "", locale),
      });
    case "days":
      return translate("reward.days", { value: promotion.value });
    case "trial":
      return translate("reward.trial", { value: promotion.value });
    default:
      return promotion.kind;
  }
}
