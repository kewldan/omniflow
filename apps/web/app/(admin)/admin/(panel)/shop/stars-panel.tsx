"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { InfoTip } from "@omniflow/ui/info-tip";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { useEnumLabel } from "@/components/admin/enum-label";
import { StateNotice } from "@/components/admin/state-notice";
import { CopyButton } from "@/components/copy-button";
import { MoneyInput } from "@/components/forms/money-input";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { parseMajorToMinor } from "@/lib/money";
import {
  formatMoney,
  type GoodsOrder,
  type GoodsProduct,
  type Listing,
  type Page,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

type StarsStatus = {
  provider: string;
  enabled: boolean;
  configured: boolean;
  dryRun: boolean;
  problem?: string;
  walletVersion: string;
  tonApiProvider: string;
  tonApiRps: number;
  walletLowStars: number;
  vpnOffer: boolean;
  currency: string;
  rate?: { minorPerTon: number; source: string; currency: string };
};

type StarsStats = {
  days: number;
  currency: string;
  ordersTotal: number;
  ordersDelivered: number;
  ordersRefunded: number;
  ordersNeedsReview: number;
  ordersInFlight: number;
  starsSold: number;
  revenueMinor: number;
  refundedMinor: number;
  costNano: number;
  costMinor?: number;
  marginMinor?: number;
  marginBps?: number;
};

type StarsWallet = {
  address: string;
  version: string;
  balanceNano: number;
  explorerUrl: string;
  price100Nano?: number;
  starsCovered?: number;
  balanceMinor?: number;
  currency?: string;
};

const PERIODS = ["1", "7", "30", "90", "365"] as const;
const STATUSES = ["all", "paid", "delivering", "delivered", "needs_review", "refunded", "failed"];
const DIRECT = "fragment_direct";

/** Nanoton as TON, trimmed the way a wallet app shows it. */
function ton(nano: number, locale: string): string {
  return `${(nano / 1e9).toLocaleString(locale, { maximumFractionDigits: 4 })} TON`;
}

/**
 * Telegram Stars sold directly on Fragment.
 *
 * This is the operator's view of the provider that pays from their own TON
 * wallet: whether it can sell at all, what the wallet holds, what the shop has
 * earned, and the orders — including the ones parked because a payment may or
 * may not have left. Nothing on this screen reveals a secret: the cookies, the
 * mnemonic, and the API key never leave the server.
 */
export function StarsPanel({ active, initialSearch }: { active: boolean; initialSearch: string }) {
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-3 lg:grid-cols-2">
        <StatusCard active={active} />
        <WalletCard active={active} />
      </div>
      <StatsCard active={active} />
      <SetupCard active={active} />
      <StarsOrders active={active} initialSearch={initialSearch} />
    </div>
  );
}

function StatusCard({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop.stars");
  const label = useEnumLabel();
  const locale = useLocale();
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();
  const {
    data,
    error: loadError,
    isLoading,
    mutate,
  } = useSWR<StarsStatus, ApiError>(active ? "/v1/panel/goods/stars/status" : null, fetcher);
  const switchId = useId();

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }
  if (loadError || !data) {
    return <StateNotice title={translate("statusUnavailable")} variant="danger" />;
  }

  async function toggle(enabled: boolean) {
    // The direct provider stores no credential, so the save carries none; its
    // secrets live in the environment only.
    const ok = await run(`/v1/panel/goods/providers/${DIRECT}`, {
      body: { enabled, spendLimitMinor: 0, spendWindowSeconds: 86_400 },
      method: "PUT",
    });
    if (ok) {
      await mutate();
    }
  }

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-medium">{translate("statusTitle")}</span>
        <div className="flex flex-wrap gap-2">
          {data.dryRun && <Badge variant="warning">{translate("dryRun")}</Badge>}
          <Badge variant={data.configured ? "success" : "danger"}>
            {translate(data.configured ? "configured" : "notConfigured")}
          </Badge>
        </div>
      </div>
      {data.dryRun && (
        <p className="text-sm text-warning-tint-foreground">{translate("dryRunExplainer")}</p>
      )}
      {data.problem && (
        <p className="font-mono text-danger-foreground text-xs" role="alert">
          {data.problem}
        </p>
      )}
      <div className="flex items-center gap-3">
        <Switch
          checked={data.enabled}
          disabled={!can("goods.write") || pending}
          id={switchId}
          onCheckedChange={(next) => void toggle(next)}
        />
        <Label htmlFor={switchId}>{translate("enabled")}</Label>
        <InfoTip>{translate("enabledHint")}</InfoTip>
      </div>
      {error && <p className="text-danger-foreground text-xs">{error.message}</p>}
      <dl className="grid grid-cols-2 gap-2 text-sm">
        <Fact label={translate("walletVersion")} value={data.walletVersion} />
        <Fact
          label={translate("tonApi")}
          value={`${data.tonApiProvider} · ${data.tonApiRps} rps`}
        />
        <Fact
          label={translate("rate")}
          value={
            data.rate
              ? `${formatMoney(data.rate.minorPerTon, data.rate.currency, locale)} · ${label("rateSource", data.rate.source)}`
              : translate("rateUnknown")
          }
        />
        <Fact label={translate("lowStars")} value={data.walletLowStars.toLocaleString(locale)} />
        <Fact label={translate("vpnOffer")} value={translate(data.vpnOffer ? "on" : "off")} />
      </dl>
      <p className="text-muted-foreground text-xs">{translate("secretsHint")}</p>
    </Card>
  );
}

function WalletCard({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop.stars");
  const locale = useLocale();
  const { data, error, isLoading, mutate } = useSWR<StarsWallet, ApiError>(
    active ? "/v1/panel/goods/stars/wallet" : null,
    fetcher,
    { revalidateOnFocus: false },
  );

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }
  if (error || !data) {
    return (
      <StateNotice
        description={error?.message}
        title={translate("walletUnavailable")}
        variant="danger"
      />
    );
  }
  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="font-medium">{translate("walletTitle")}</span>
        <Button onClick={() => void mutate()} size="sm" variant="outline">
          {translate("refresh")}
        </Button>
      </div>
      <div className="flex items-center gap-2">
        <a
          className="break-all font-mono text-xs underline-offset-2 hover:underline"
          href={data.explorerUrl}
          rel="noreferrer"
          target="_blank"
        >
          {data.address}
        </a>
        <CopyButton label={translate("copyAddress")} value={data.address} />
      </div>
      <dl className="grid grid-cols-2 gap-2 text-sm">
        <Fact
          label={translate("balance")}
          value={
            ton(data.balanceNano, locale) +
            (data.balanceMinor !== undefined && data.currency
              ? ` ≈ ${formatMoney(data.balanceMinor, data.currency, locale)}`
              : "")
          }
        />
        <Fact
          label={translate("price100")}
          value={data.price100Nano ? ton(data.price100Nano, locale) : "—"}
        />
        <Fact
          label={translate("covers")}
          value={
            data.starsCovered !== undefined
              ? translate("coversStars", { count: data.starsCovered })
              : "—"
          }
        />
        <Fact label={translate("walletVersion")} value={data.version} />
      </dl>
    </Card>
  );
}

function StatsCard({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop.stars");
  const locale = useLocale();
  const [days, setDays] = useState<string>("30");
  const { data, isLoading } = useSWR<StarsStats, ApiError>(
    active ? `/v1/panel/goods/stars/stats${toQuery({ days })}` : null,
    fetcher,
  );

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-medium">{translate("statsTitle")}</span>
        <Select onValueChange={setDays} value={days}>
          <SelectTrigger aria-label={translate("period")} className="w-auto min-w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PERIODS.map((period) => (
              <SelectItem key={period} value={period}>
                {translate("days", { count: Number(period) })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {isLoading || !data ? (
        <Skeleton className="h-20 w-full" />
      ) : (
        <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
          <Fact
            label={translate("revenue")}
            value={formatMoney(data.revenueMinor, data.currency, locale)}
          />
          <Fact label={translate("starsSold")} value={data.starsSold.toLocaleString(locale)} />
          <Fact
            label={translate("margin")}
            value={
              data.marginMinor !== undefined
                ? `${formatMoney(data.marginMinor, data.currency, locale)}${
                    data.marginBps !== undefined
                      ? ` · ${(data.marginBps / 100).toLocaleString(locale, { maximumFractionDigits: 1 })}%`
                      : ""
                  }`
                : translate("unknown")
            }
          />
          <Fact
            label={translate("cost")}
            value={
              data.costMinor !== undefined
                ? `${formatMoney(data.costMinor, data.currency, locale)} · ${ton(data.costNano, locale)}`
                : ton(data.costNano, locale)
            }
          />
          <Fact
            label={translate("delivered")}
            value={`${data.ordersDelivered.toLocaleString(locale)} / ${data.ordersTotal.toLocaleString(locale)}`}
          />
          <Fact
            label={translate("refunds")}
            value={`${data.ordersRefunded} · ${formatMoney(data.refundedMinor, data.currency, locale)}`}
          />
          <Fact label={translate("inFlight")} value={String(data.ordersInFlight)} />
          <Fact label={translate("needsReview")} value={String(data.ordersNeedsReview)} />
        </dl>
      )}
    </Card>
  );
}

/**
 * Creates the custom-amount Stars product the bot's «Buy Stars» button and the
 * web shop sell: bounds, presets, a fixed price per Star, and its name in both
 * languages. It appears only while no such product exists.
 */
function SetupCard({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop.stars.setup");
  const { can } = useSession();
  const { data, mutate } = useSWR<Listing<GoodsProduct>, ApiError>(
    active ? "/v1/panel/goods/products" : null,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const [values, setValues] = useState({
    code: "stars",
    currency: "RUB",
    max: "10000",
    min: "50",
    nameEn: "Telegram Stars",
    nameRu: "Звёзды Telegram",
    presets: "50, 100, 250, 500, 1000",
    price: "1.50",
  });
  const [problem, setProblem] = useState("");

  const exists = (data?.items ?? []).some(
    (product) =>
      product.kind === "telegram_stars" &&
      product.providerSlug === DIRECT &&
      product.maxStarQuantity !== undefined &&
      !product.archivedAt,
  );
  if (!data || exists || !can("goods.write")) {
    return null;
  }

  async function create() {
    setProblem("");
    const min = Number(values.min);
    const max = Number(values.max);
    const presets = values.presets
      .split(",")
      .map((entry) => Number(entry.trim()))
      .filter((entry) => Number.isInteger(entry) && entry > 0);
    const price = parseMajorToMinor(values.price, values.currency.trim().toUpperCase());
    if (!Number.isInteger(min) || !Number.isInteger(max) || min < 50 || max < min) {
      setProblem(translate("badBounds"));
      return;
    }
    if (presets.some((preset) => preset < min || preset > max)) {
      setProblem(translate("badPresets"));
      return;
    }
    if (!price.ok || price.minor <= 0) {
      setProblem(translate("badPrice"));
      return;
    }
    const created = await run("/v1/panel/goods/products", {
      body: {
        code: values.code.trim(),
        kind: "telegram_stars",
        maxStarQuantity: max,
        minStarQuantity: min,
        providerSlug: DIRECT,
        sortOrder: 0,
        starPresets: presets,
        visible: false,
      },
      method: "POST",
    });
    if (!created) {
      return;
    }
    const refreshed = await mutate();
    const product = refreshed?.items?.find((item) => item.code === values.code.trim());
    if (!product) {
      return;
    }
    await run(`/v1/panel/goods/products/${product.id}/localizations/ru`, {
      body: { description: "", name: values.nameRu },
      method: "PUT",
    });
    await run(`/v1/panel/goods/products/${product.id}/localizations/en`, {
      body: { description: "", name: values.nameEn },
      method: "PUT",
    });
    await run(`/v1/panel/goods/products/${product.id}/pricing`, {
      body: {
        currency: values.currency.trim().toUpperCase(),
        markupBps: 0,
        quoteTtlSeconds: 600,
        rounding: "none",
        unitPriceMinor: price.minor,
      },
      method: "PUT",
    });
    await run(`/v1/panel/goods/products/${product.id}`, {
      body: { archived: false, sortOrder: 0, visible: true },
      method: "PATCH",
    });
    await mutate();
  }

  const field = (key: keyof typeof values, label: string, numeric = false) => (
    <div className="flex flex-col gap-1">
      <Label htmlFor={`stars-setup-${key}`}>{label}</Label>
      <Input
        id={`stars-setup-${key}`}
        inputMode={numeric ? "numeric" : undefined}
        onChange={(event) => setValues({ ...values, [key]: event.target.value })}
        value={values[key]}
      />
    </div>
  );

  return (
    <Card className="flex flex-col gap-3 p-4">
      <span className="font-medium">{translate("title")}</span>
      <p className="text-muted-foreground text-sm">{translate("description")}</p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {field("code", translate("code"))}
        {field("min", translate("min"), true)}
        {field("max", translate("max"), true)}
        {field("presets", translate("presets"))}
        {field("currency", translate("currency"))}
        <div className="flex flex-col gap-1">
          <Label htmlFor="stars-setup-price">{translate("price")}</Label>
          <MoneyInput
            currency={values.currency}
            id="stars-setup-price"
            onChange={(event) => setValues({ ...values, price: event.target.value })}
            value={values.price}
          />
        </div>
        {field("nameRu", translate("nameRu"))}
        {field("nameEn", translate("nameEn"))}
      </div>
      {(problem || error) && (
        <p className="text-danger-foreground text-sm" role="alert">
          {problem || error?.message}
        </p>
      )}
      <Button className="self-start" disabled={pending} onClick={() => void create()} size="sm">
        {translate("create")}
      </Button>
    </Card>
  );
}

type Decision = { kind: "retry" | "refund" | "complete"; order: GoodsOrder };

function StarsOrders({ active, initialSearch }: { active: boolean; initialSearch: string }) {
  const translate = useTranslations("admin.shop");
  const translateStars = useTranslations("admin.shop.stars");
  const locale = useLocale();
  const { can } = useSession();
  const [status, setStatus] = useState("all");
  const [search, setSearch] = useState(initialSearch);
  const [decision, setDecision] = useState<Decision | null>(null);

  const key = active
    ? `/v1/panel/goods/orders${toQuery({
        kind: "telegram_stars",
        q: search.trim() || undefined,
        status: status === "all" ? undefined : status,
      })}`
    : null;
  const { data, isLoading, mutate } = useSWR<Page<GoodsOrder>, ApiError>(key, fetcher);
  const items = data?.items ?? [];

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-auto font-medium">{translateStars("ordersTitle")}</span>
        <Input
          aria-label={translateStars("search")}
          className="w-64"
          onChange={(event) => setSearch(event.target.value)}
          placeholder={translateStars("search")}
          value={search}
        />
        <Select onValueChange={setStatus} value={status}>
          <SelectTrigger aria-label={translateStars("statusFilter")} className="w-auto min-w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATUSES.map((value) => (
              <SelectItem key={value} value={value}>
                {value === "all" ? translateStars("allStatuses") : translate(`status.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {isLoading ? (
        <Skeleton className="h-32 w-full" />
      ) : items.length === 0 ? (
        <StateNotice title={translate("empty.orders")} variant="empty" />
      ) : (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("columns.createdAt")}</TableHead>
                <TableHead>{translate("columns.recipient")}</TableHead>
                <TableHead>{translateStars("stars")}</TableHead>
                <TableHead>{translate("columns.price")}</TableHead>
                <TableHead>{translate("columns.margin")}</TableHead>
                <TableHead>{translate("columns.status")}</TableHead>
                <TableHead>{translateStars("payment")}</TableHead>
                {can("goods.manage") && <TableHead>{translate("columns.actions")}</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((order) => (
                <TableRow key={order.orderId}>
                  <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                    <Timestamp value={order.createdAt} />
                    <div>
                      <Link
                        className="underline-offset-2 hover:underline"
                        href={`/admin/customers/${order.customerId}`}
                      >
                        {order.orderId.slice(0, 8)}
                      </Link>
                      {" · "}
                      {translateStars(`source.${order.source}`)}
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-[12px]">
                    @{order.recipient}
                    {order.recipientIsSelf && (
                      <Badge className="ml-2" variant="neutral">
                        {translate("self")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell data-numeric>
                    {order.starQuantity?.toLocaleString(locale) ?? "—"}
                  </TableCell>
                  <TableCell data-numeric>
                    {formatMoney(order.paidMinor, order.currency, locale)}
                  </TableCell>
                  <TableCell data-numeric>
                    {order.marginMinor !== undefined
                      ? formatMoney(order.marginMinor, order.currency, locale)
                      : "—"}
                  </TableCell>
                  <TableCell>
                    <Badge variant={statusVariant(order.status)}>
                      {translate(`status.${order.status}`)}
                    </Badge>
                    {order.dryRun && (
                      <Badge className="ml-1" variant="warning">
                        {translateStars("dryRun")}
                      </Badge>
                    )}
                    {order.errorCode && (
                      <div className="font-mono text-[10px] text-muted-foreground">
                        {order.errorCode}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="font-mono text-[11px]">
                    {order.costNano ? <div>{ton(order.costNano, locale)}</div> : null}
                    {order.txHash && !order.dryRun ? (
                      <a
                        className="underline-offset-2 hover:underline"
                        href={`https://tonviewer.com/transaction/${order.txHash}`}
                        rel="noreferrer"
                        target="_blank"
                      >
                        {order.txHash.slice(0, 10)}…
                      </a>
                    ) : null}
                    {order.providerConfirmed === false && (
                      <div className="text-warning-tint-foreground">
                        {translateStars("unconfirmed")}
                      </div>
                    )}
                  </TableCell>
                  {can("goods.manage") && (
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {order.status === "needs_review" && (
                          <>
                            <Button
                              onClick={() => setDecision({ kind: "complete", order })}
                              size="sm"
                              variant="outline"
                            >
                              {translateStars("actions.complete")}
                            </Button>
                            <Button
                              onClick={() => setDecision({ kind: "retry", order })}
                              size="sm"
                              variant="outline"
                            >
                              {translateStars("actions.retry")}
                            </Button>
                          </>
                        )}
                        {(order.status === "needs_review" ||
                          (order.status === "paid" && !order.broadcastingAt)) && (
                          <Button
                            onClick={() => setDecision({ kind: "refund", order })}
                            size="sm"
                            variant="destructive"
                          >
                            {translateStars("actions.refund")}
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {decision && (
        <DecisionDialog
          decision={decision}
          onClose={() => setDecision(null)}
          onDone={async () => {
            setDecision(null);
            await mutate();
          }}
        />
      )}
    </Card>
  );
}

function statusVariant(status: string) {
  switch (status) {
    case "delivered":
      return "success" as const;
    case "needs_review":
      return "warning" as const;
    case "failed":
      return "danger" as const;
    default:
      return "neutral" as const;
  }
}

/**
 * One decision about one order's money. Every action needs a reason, because
 * each is a judgement made on evidence outside Omniflow — the wallet's outgoing
 * transfers — that somebody may later have to defend.
 */
function DecisionDialog({
  decision,
  onClose,
  onDone,
}: {
  decision: Decision;
  onClose: () => void;
  onDone: () => Promise<void>;
}) {
  const translate = useTranslations("admin.shop.stars.actions");
  const [reason, setReason] = useState("");
  const [txHash, setTxHash] = useState(decision.order.txHash ?? "");
  const { run, pending, error } = useOperatorAction();
  const reasonId = useId();
  const hashId = useId();

  async function submit() {
    const ok = await run(`/v1/panel/goods/orders/${decision.order.orderId}/${decision.kind}`, {
      body: decision.kind === "complete" ? { txHash: txHash.trim() } : undefined,
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      await onDone();
    }
  }

  return (
    <Dialog onOpenChange={(open) => !open && onClose()} open>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{translate(`${decision.kind}Title`)}</DialogTitle>
          <DialogDescription>{translate(`${decision.kind}Explainer`)}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <p className="font-mono text-xs">
            @{decision.order.recipient} · {decision.order.starQuantity ?? "—"} ⭐ ·{" "}
            {decision.order.orderId}
          </p>
          {decision.order.providerRequestId && (
            <p className="font-mono text-muted-foreground text-xs">
              Fragment: {decision.order.providerRequestId}
            </p>
          )}
          {decision.kind === "complete" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor={hashId}>{translate("txHash")}</Label>
              <Input
                id={hashId}
                onChange={(event) => setTxHash(event.target.value)}
                value={txHash}
              />
            </div>
          )}
          <div className="flex flex-col gap-1">
            <Label htmlFor={reasonId}>{translate("reason")}</Label>
            <Input
              id={reasonId}
              onChange={(event) => setReason(event.target.value)}
              value={reason}
            />
          </div>
          {error && (
            <p className="text-danger-foreground text-sm" role="alert">
              {error.message}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button onClick={onClose} variant="outline">
            {translate("cancel")}
          </Button>
          <Button
            disabled={pending || reason.trim().length === 0}
            onClick={() => void submit()}
            variant={decision.kind === "refund" ? "destructive" : "default"}
          >
            {translate(`${decision.kind}Confirm`)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <dt className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </dt>
      <dd className="tabular-nums">{value}</dd>
    </div>
  );
}
