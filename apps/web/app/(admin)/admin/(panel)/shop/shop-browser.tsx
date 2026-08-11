"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  formatMoney,
  type GoodsOrder,
  type GoodsProduct,
  type Listing,
  type Page,
} from "@/lib/operations";

type GoodsProvider = {
  slug: string;
  enabled: boolean;
  credentialsSet: boolean;
  balanceMinor?: number;
  balanceCurrency?: string;
  lowBalanceThresholdMinor?: number;
  lowBalance: boolean;
  spendLimitMinor: number;
  status: string;
  lastErrorCode?: string;
};

/**
 * The digital-goods shop.
 *
 * A digital good is not VPN access: nothing on this page touches an
 * entitlement, a subscription, or Remnawave. What it does touch is the
 * operator's own funded balance with the provider, which is why the provider
 * tab leads with that number and flags it before it runs out.
 */
export function ShopBrowser() {
  const translate = useTranslations("admin.shop");
  const [tab, setTab] = useState("orders");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="orders">{translate("tabs.orders")}</TabsTrigger>
          <TabsTrigger value="products">{translate("tabs.products")}</TabsTrigger>
          <TabsTrigger value="providers">{translate("tabs.providers")}</TabsTrigger>
        </TabsList>
        <TabsContent value="orders">
          <Orders active={tab === "orders"} />
        </TabsContent>
        <TabsContent value="products">
          <Products active={tab === "products"} />
        </TabsContent>
        <TabsContent value="providers">
          <Providers active={tab === "providers"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Orders({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Page<GoodsOrder>, ApiError>(
    active ? "/v1/panel/goods/orders" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice title={translate("empty.orders")} variant="empty" />
      </Card>
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.createdAt")}</TableHead>
            <TableHead>{translate("columns.recipient")}</TableHead>
            <TableHead>{translate("columns.price")}</TableHead>
            <TableHead>{translate("columns.margin")}</TableHead>
            <TableHead>{translate("columns.status")}</TableHead>
            <TableHead>{translate("columns.delivery")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((order) => (
            <TableRow key={order.orderId}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(order.createdAt).toLocaleString(locale)}
              </TableCell>
              <TableCell className="font-mono text-[12px]">
                {/* The username is the only recipient detail retained anywhere:
                    delivery needs it and support needs to answer "where did it
                    go". Nothing else about the recipient is stored. */}
                @{order.recipient}
                {order.recipientIsSelf && (
                  <Badge className="ml-2" variant="neutral">
                    {translate("self")}
                  </Badge>
                )}
              </TableCell>
              <TableCell data-numeric>
                {formatMoney(order.quotedPriceMinor, order.currency, locale)}
              </TableCell>
              <TableCell data-numeric>
                {formatMoney(order.marginMinor, order.currency, locale)}
              </TableCell>
              <TableCell>
                <Badge
                  variant={
                    order.status === "delivered"
                      ? "success"
                      : order.status === "failed"
                        ? "danger"
                        : "neutral"
                  }
                >
                  {translate(`status.${order.status}`)}
                </Badge>
              </TableCell>
              <TableCell className="font-mono text-[11px] text-muted-foreground">
                {order.failureClass
                  ? translate(`failure.${order.failureClass}`)
                  : (order.deliveryStatus ?? "—")}
                {order.refunded ? ` · ${translate("refunded")}` : ""}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Products({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Listing<GoodsProduct>, ApiError>(
    active ? "/v1/panel/goods/products" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice
          description={translate("empty.productsDescription")}
          title={translate("empty.products")}
          variant="empty"
        />
      </Card>
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.code")}</TableHead>
            <TableHead>{translate("columns.kind")}</TableHead>
            <TableHead>{translate("columns.quantity")}</TableHead>
            <TableHead>{translate("columns.pricing")}</TableHead>
            <TableHead>{translate("columns.visible")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((product) => (
            <TableRow key={product.id}>
              <TableCell className="font-mono text-[12px]">{product.code}</TableCell>
              <TableCell>{translate(`kind.${product.kind}`)}</TableCell>
              <TableCell data-numeric>
                {product.durationMonths
                  ? translate("months", { count: product.durationMonths })
                  : (product.starQuantity?.toLocaleString(locale) ?? "—")}
              </TableCell>
              <TableCell className="text-muted-foreground text-sm">
                {product.pricing
                  ? product.pricing.fixedAmountMinor
                    ? translate("pricing.fixed", {
                        amount: formatMoney(
                          product.pricing.fixedAmountMinor,
                          product.pricing.currency,
                          locale,
                        ),
                      })
                    : translate("pricing.markup", {
                        percent: (product.pricing.markupBps / 100).toFixed(2),
                        rounding: translate(`rounding.${product.pricing.rounding}`),
                      })
                  : translate("pricing.missing")}
              </TableCell>
              <TableCell>
                <Badge variant={product.visible && !product.archivedAt ? "success" : "neutral"}>
                  {translate(
                    product.archivedAt ? "archived" : product.visible ? "visible" : "hidden",
                  )}
                </Badge>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Providers({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Listing<GoodsProvider>, ApiError>(
    active ? "/v1/panel/goods/providers" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice
          description={translate("empty.providersDescription")}
          title={translate("empty.providers")}
          variant="empty"
        />
      </Card>
    );
  }

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      {items.map((provider) => (
        <Card className="flex flex-col gap-2 p-4" key={provider.slug}>
          <div className="flex items-center justify-between gap-3">
            <span className="font-medium">{provider.slug}</span>
            <div className="flex gap-2">
              {provider.lowBalance && <Badge variant="danger">{translate("lowBalance")}</Badge>}
              <Badge
                variant={
                  provider.status === "healthy"
                    ? "success"
                    : provider.status === "failing"
                      ? "danger"
                      : "neutral"
                }
              >
                {provider.status}
              </Badge>
            </div>
          </div>
          <dl className="grid grid-cols-2 gap-1 text-sm">
            <Row
              label={translate("provider.balance")}
              value={
                provider.balanceMinor !== undefined && provider.balanceCurrency
                  ? formatMoney(provider.balanceMinor, provider.balanceCurrency, locale)
                  : "—"
              }
            />
            <Row
              label={translate("provider.spendLimit")}
              value={
                provider.spendLimitMinor > 0 && provider.balanceCurrency
                  ? formatMoney(provider.spendLimitMinor, provider.balanceCurrency, locale)
                  : translate("provider.noLimit")
              }
            />
            <Row
              label={translate("provider.credentials")}
              value={translate(provider.credentialsSet ? "provider.set" : "provider.unset")}
            />
            <Row
              label={translate("provider.enabled")}
              value={translate(provider.enabled ? "yes" : "no")}
            />
          </dl>
          {provider.lastErrorCode && (
            <p className="font-mono text-danger-foreground text-xs">{provider.lastErrorCode}</p>
          )}
        </Card>
      ))}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </dt>
      <dd className="tabular-nums">{value}</dd>
    </div>
  );
}
