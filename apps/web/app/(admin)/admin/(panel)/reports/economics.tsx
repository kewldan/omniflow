"use client";

import type {
  PanelCurrencyEconomics,
  PanelEconomicsReport,
  PanelMoney,
  PanelOperatingCost,
  PanelProviderFee,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Trash2 } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { useSubmission } from "@/lib/idempotency";
import { parseMajorToMinor } from "@/lib/money";
import { formatMoney, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const CATEGORIES = ["servers", "software", "staff", "other"] as const;
type Category = (typeof CATEGORIES)[number];

/**
 * What the period earned and what it cost.
 *
 * Every line is one currency: provider money, refunds, what payment providers
 * kept, running costs, advertising, and referral rewards, down to the
 * contribution left. Nothing is converted, so a cost in euros never reduces a
 * margin in roubles. Running costs and provider rates are entered here by
 * hand — Omniflow sees neither the server bill nor the provider's statement.
 */
export function EconomicsReportScreen({ query }: { query: string }) {
  const translate = useTranslations("admin.economics");
  const locale = useLocale();
  const { data, error, isLoading, mutate } = useSWR<PanelEconomicsReport, ApiError>(
    `/v1/panel/reports/economics${query}`,
    fetcher,
  );

  if (error) {
    return <StateNotice description={error.message} title={translate("failed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-96 w-full" />;
  }

  const refresh = () => void mutate();

  return (
    <>
      {data.currencies.length === 0 ? (
        <StateNotice description={translate("emptyDescription")} title={translate("empty")} />
      ) : (
        data.currencies.map((line) => (
          <ProfitCard
            averageSubscribers={data.averageSubscribers}
            key={line.currency}
            line={line}
            locale={locale}
          />
        ))
      )}

      <Card>
        <CardHeader>
          <CardTitle>{translate("providers.title")}</CardTitle>
          <CardDescription>{translate("providers.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {data.providers.length === 0 ? (
            <StateNotice description={translate("providers.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("providers.provider")}</TableHead>
                  <TableHead className="text-right">{translate("providers.payments")}</TableHead>
                  <TableHead className="text-right">{translate("providers.paid")}</TableHead>
                  <TableHead className="text-right">{translate("providers.fee")}</TableHead>
                  <TableHead className="text-right">{translate("providers.rate")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.providers.map((line) => (
                  <TableRow key={`${line.provider}-${line.currency}`}>
                    <TableCell className="font-mono text-xs">
                      {line.provider} <Badge variant="neutral">{line.currency}</Badge>
                      {line.withoutRate > 0 ? (
                        <Badge className="ml-1" variant="warning">
                          {translate("providers.withoutRate", { count: line.withoutRate })}
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{line.payments}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.paidMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.feeMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(line.effectiveRate, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("nodes.title")}</CardTitle>
          <CardDescription>{translate("nodes.description")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4 overflow-x-auto">
          <p className="text-sm tabular-nums">
            {translate("nodes.total", { traffic: gibibytes(data.trafficBytes, locale) })}
            {data.serverCostPerGibibyte.length > 0
              ? ` · ${translate("nodes.serverPerGib", {
                  cost: moneyList(data.serverCostPerGibibyte, locale),
                })}`
              : ""}
          </p>
          {data.nodes.length === 0 ? (
            <StateNotice description={translate("nodes.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("nodes.node")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.traffic")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.paying")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.cost")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.perGib")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.nodes.map((node) => (
                  <TableRow key={node.nodeUuid}>
                    <TableCell>
                      <div className="font-medium">{node.nodeName || node.nodeUuid}</div>
                      <div className="text-muted-foreground font-mono text-xs">
                        {node.countryCode}
                      </div>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {gibibytes(node.trafficBytes, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {node.payingCustomers}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {moneyList(node.costs, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {moneyList(node.costPerGibibyte, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <CostsCard nodes={data.nodes} onChange={refresh} query={query} />
      <ProviderFeesCard onChange={refresh} seen={data.providers.map((line) => line.provider)} />
    </>
  );
}

/** One currency's profit and loss, from provider money down to contribution. */
function ProfitCard({
  averageSubscribers,
  line,
  locale,
}: {
  averageSubscribers?: number;
  line: PanelCurrencyEconomics;
  locale: string;
}) {
  const translate = useTranslations("admin.economics.profit");
  const money = (minor: number) => formatMoney(minor, line.currency, locale);
  // A deduction is shown with its minus sign, but nothing is not negative.
  const deduct = (minor: number) => (minor === 0 ? money(0) : money(-minor));
  const rows: { key: string; value: string; strong?: boolean; negative?: boolean }[] = [
    { key: "provider", value: money(line.providerMinor) },
    { key: "refunds", value: deduct(line.refundsMinor), negative: true },
    { key: "fees", value: deduct(line.feesMinor), negative: true },
    { key: "net", value: money(line.netRevenueMinor), strong: true },
    { key: "operating", value: deduct(line.operatingMinor), negative: true },
    { key: "acquisition", value: deduct(line.acquisitionMinor), negative: true },
    { key: "referral", value: deduct(line.referralMinor), negative: true },
    { key: "contribution", value: money(line.contributionMinor), strong: true },
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {translate("title")} <Badge variant="neutral">{line.currency}</Badge>
          {line.margin === undefined ? null : (
            <Badge variant={line.margin >= 0 ? "success" : "danger"}>
              {translate("margin", { value: percent(line.margin, locale) })}
            </Badge>
          )}
        </CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6 lg:grid-cols-2">
        <dl className="flex flex-col gap-1 text-sm">
          {rows.map((row) => (
            <div
              className={`flex justify-between gap-4 ${row.strong ? "border-t pt-1 font-semibold" : ""}`}
              key={row.key}
            >
              <dt className={row.negative ? "text-muted-foreground" : undefined}>
                {translate(row.key)}
              </dt>
              <dd className="tabular-nums">{row.value}</dd>
            </div>
          ))}
        </dl>
        <div className="flex flex-col gap-3 text-sm">
          {line.feesEstimated ? (
            <StateNotice description={translate("feesMissing")} title={translate("estimate")} />
          ) : null}
          <div className="flex justify-between gap-4">
            <span>{translate("subscribers")}</span>
            <span className="tabular-nums">
              {averageSubscribers === undefined
                ? "—"
                : averageSubscribers.toLocaleString(locale, { maximumFractionDigits: 1 })}
            </span>
          </div>
          <div className="flex justify-between gap-4">
            <span>{translate("revenuePerSubscriber")}</span>
            <span className="tabular-nums">
              {line.revenuePerSubscriberMinor === undefined
                ? "—"
                : money(line.revenuePerSubscriberMinor)}
            </span>
          </div>
          <div className="flex justify-between gap-4">
            <span>{translate("costPerSubscriber")}</span>
            <span className="tabular-nums">
              {line.costPerSubscriberMinor === undefined ? "—" : money(line.costPerSubscriberMinor)}
            </span>
          </div>
          <p className="text-muted-foreground text-xs">{translate("perSubscriberHint")}</p>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * Running costs whose span touches the period, and the form that records one.
 * Reading needs finance.read; recording and removing need finance.write, which
 * the API enforces whatever this form shows.
 */
function CostsCard({
  nodes,
  onChange,
  query,
}: {
  nodes: PanelEconomicsReport["nodes"];
  onChange: () => void;
  query: string;
}) {
  const translate = useTranslations("admin.economics.costs");
  const period = useTranslations("admin.reports.period");
  const locale = useLocale();
  const { can } = useSession();
  const editable = can("finance.write");
  const { data, mutate } = useSWR<{ items: PanelOperatingCost[] }, ApiError>(
    `/v1/panel/reports/costs${query}`,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const submission = useSubmission();
  const ids = {
    category: useId(),
    node: useId(),
    from: useId(),
    to: useId(),
    currency: useId(),
    amount: useId(),
    note: useId(),
  };
  const [category, setCategory] = useState<Category>("servers");
  const [node, setNode] = useState("none");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [currency, setCurrency] = useState("RUB");
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  const [problem, setProblem] = useState("");

  async function record(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const parsed = parseMajorToMinor(amount, currency);
    if (!parsed.ok || parsed.minor <= 0) {
      setProblem(translate("invalidAmount"));
      return;
    }
    if (!from || !to || to.slice(0, 10) < from.slice(0, 10)) {
      setProblem(translate("invalidSpan"));
      return;
    }
    setProblem("");
    const ok = await run("/v1/panel/reports/costs", {
      body: {
        category,
        nodeUuid: node === "none" ? undefined : node,
        coversFrom: from.slice(0, 10),
        coversTo: to.slice(0, 10),
        currency: currency.trim().toUpperCase(),
        amountMinor: parsed.minor,
        note: note.trim(),
      },
      method: "POST",
      reason: "running cost",
      submission,
    });
    if (ok) {
      setAmount("");
      setNote("");
      void mutate();
      onChange();
    }
  }

  async function remove(id: string) {
    const ok = await run(`/v1/panel/reports/costs/${id}`, {
      method: "DELETE",
      reason: "running cost entered by mistake",
    });
    if (ok) {
      void mutate();
      onChange();
    }
  }

  const items = data?.items ?? [];
  const nodeName = (uuid?: string) =>
    uuid ? (nodes.find((candidate) => candidate.nodeUuid === uuid)?.nodeName ?? uuid) : "";

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {editable ? (
          <form className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" onSubmit={record}>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.category}>{translate("category")}</Label>
              <Select onValueChange={(value) => setCategory(value as Category)} value={category}>
                <SelectTrigger id={ids.category}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CATEGORIES.map((value) => (
                    <SelectItem key={value} value={value}>
                      {translate(`categories.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.node}>{translate("node")}</Label>
              <Select onValueChange={setNode} value={node}>
                <SelectTrigger id={ids.node}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{translate("noNode")}</SelectItem>
                  {nodes.map((candidate) => (
                    <SelectItem key={candidate.nodeUuid} value={candidate.nodeUuid}>
                      {candidate.nodeName || candidate.nodeUuid}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.amount}>{translate("amount")}</Label>
              <div className="flex gap-2">
                <Input
                  className="w-20"
                  id={ids.currency}
                  maxLength={3}
                  onChange={(event) => setCurrency(event.target.value.toUpperCase())}
                  aria-label={translate("currency")}
                  required
                  value={currency}
                />
                <Input
                  id={ids.amount}
                  inputMode="decimal"
                  onChange={(event) => setAmount(event.target.value)}
                  required
                  value={amount}
                />
              </div>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.from}>{translate("from")}</Label>
              <DateTimeField
                hourLabel={period("hour")}
                id={ids.from}
                minuteLabel={period("minute")}
                onChange={setFrom}
                placeholder={translate("from")}
                value={from}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.to}>{translate("to")}</Label>
              <DateTimeField
                hourLabel={period("hour")}
                id={ids.to}
                minuteLabel={period("minute")}
                onChange={setTo}
                placeholder={translate("to")}
                value={to}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.note}>{translate("note")}</Label>
              <Input
                id={ids.note}
                maxLength={200}
                onChange={(event) => setNote(event.target.value)}
                value={note}
              />
            </div>
            <div className="flex items-end gap-3 sm:col-span-2 lg:col-span-3">
              <Button disabled={pending} type="submit">
                {translate("record")}
              </Button>
              {problem || error ? (
                <span className="text-destructive text-sm" role="alert">
                  {problem || error?.message}
                </span>
              ) : null}
            </div>
          </form>
        ) : null}
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("span")}</TableHead>
                <TableHead>{translate("category")}</TableHead>
                <TableHead className="text-right">{translate("amount")}</TableHead>
                <TableHead>{translate("note")}</TableHead>
                {editable ? <TableHead className="sr-only">{translate("remove")}</TableHead> : null}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 ? (
                <TableRow>
                  <TableCell className="text-muted-foreground" colSpan={editable ? 5 : 4}>
                    {translate("empty")}
                  </TableCell>
                </TableRow>
              ) : (
                items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell className="whitespace-nowrap font-mono text-xs">
                      {item.coversFrom} — {item.coversTo}
                    </TableCell>
                    <TableCell>
                      {translate(`categories.${item.category}`)}
                      {item.nodeUuid ? (
                        <span className="text-muted-foreground block text-xs">
                          {nodeName(item.nodeUuid)}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(item.amountMinor, item.currency, locale)}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-sm">{item.note}</TableCell>
                    {editable ? (
                      <TableCell className="text-right">
                        <Button
                          aria-label={translate("remove")}
                          disabled={pending}
                          onClick={() => void remove(item.id)}
                          size="icon"
                          variant="ghost"
                        >
                          <Trash2 aria-hidden />
                        </Button>
                      </TableCell>
                    ) : null}
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * What each payment provider keeps, as the operator's contract states it. A
 * rate applies from its date on, so entering a new one never rewrites an
 * earlier month's margin.
 */
function ProviderFeesCard({ onChange, seen }: { onChange: () => void; seen: string[] }) {
  const translate = useTranslations("admin.economics.fees");
  const period = useTranslations("admin.reports.period");
  const locale = useLocale();
  const { can } = useSession();
  const editable = can("finance.write");
  const { data, mutate } = useSWR<{ items: PanelProviderFee[] }, ApiError>(
    "/v1/panel/reports/provider-fees",
    fetcher,
  );
  const { data: configured } = useSWR<{ items: { provider: string }[] | null }, ApiError>(
    editable && can("settings.read") ? "/v1/panel/settings/providers" : null,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const ids = {
    provider: useId(),
    from: useId(),
    percent: useId(),
    fixed: useId(),
    fixedCurrency: useId(),
  };
  const [provider, setProvider] = useState("");
  const [from, setFrom] = useState("");
  const [share, setShare] = useState("");
  const [fixed, setFixed] = useState("");
  const [fixedCurrency, setFixedCurrency] = useState("RUB");
  const [problem, setProblem] = useState("");

  const items = data?.items ?? [];
  const providers = Array.from(
    new Set([
      ...seen.filter((value) => value !== "unknown"),
      ...items.map((item) => item.provider),
      ...(configured?.items ?? []).map((item) => item.provider),
    ]),
  ).sort();

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const percentValue = Number(share.replace(",", "."));
    if (
      !provider ||
      !from ||
      !Number.isFinite(percentValue) ||
      percentValue < 0 ||
      percentValue > 100
    ) {
      setProblem(translate("invalid"));
      return;
    }
    let fixedMinor = 0;
    if (fixed.trim() !== "") {
      const parsed = parseMajorToMinor(fixed, fixedCurrency);
      if (!parsed.ok || parsed.minor < 0) {
        setProblem(translate("invalid"));
        return;
      }
      fixedMinor = parsed.minor;
    }
    setProblem("");
    const ok = await run("/v1/panel/reports/provider-fees", {
      body: {
        provider,
        effectiveFrom: from.slice(0, 10),
        percentBp: Math.round(percentValue * 100),
        fixedMinor,
        fixedCurrency: fixedMinor > 0 ? fixedCurrency.trim().toUpperCase() : undefined,
      },
      method: "POST",
      reason: "payment provider rate",
    });
    if (ok) {
      setShare("");
      setFixed("");
      void mutate();
      onChange();
    }
  }

  async function remove(id: string) {
    const ok = await run(`/v1/panel/reports/provider-fees/${id}`, {
      method: "DELETE",
      reason: "payment provider rate entered by mistake",
    });
    if (ok) {
      void mutate();
      onChange();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {editable ? (
          <form className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4" onSubmit={save}>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.provider}>{translate("provider")}</Label>
              <Select onValueChange={setProvider} value={provider}>
                <SelectTrigger id={ids.provider}>
                  <SelectValue placeholder={translate("providerPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {providers.map((value) => (
                    <SelectItem key={value} value={value}>
                      {value}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.from}>{translate("from")}</Label>
              <DateTimeField
                hourLabel={period("hour")}
                id={ids.from}
                minuteLabel={period("minute")}
                onChange={setFrom}
                placeholder={translate("from")}
                value={from}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.percent}>{translate("percent")}</Label>
              <Input
                id={ids.percent}
                inputMode="decimal"
                onChange={(event) => setShare(event.target.value)}
                placeholder="3.5"
                required
                value={share}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.fixed}>{translate("fixed")}</Label>
              <div className="flex gap-2">
                <Input
                  aria-label={translate("fixedCurrency")}
                  className="w-20"
                  id={ids.fixedCurrency}
                  maxLength={3}
                  onChange={(event) => setFixedCurrency(event.target.value.toUpperCase())}
                  value={fixedCurrency}
                />
                <Input
                  id={ids.fixed}
                  inputMode="decimal"
                  onChange={(event) => setFixed(event.target.value)}
                  placeholder="0"
                  value={fixed}
                />
              </div>
            </div>
            <div className="flex items-end gap-3 sm:col-span-2 lg:col-span-4">
              <Button disabled={pending || !provider} type="submit">
                {translate("save")}
              </Button>
              {problem || error ? (
                <span className="text-destructive text-sm" role="alert">
                  {problem || error?.message}
                </span>
              ) : null}
            </div>
          </form>
        ) : null}
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("provider")}</TableHead>
                <TableHead>{translate("from")}</TableHead>
                <TableHead className="text-right">{translate("percent")}</TableHead>
                <TableHead className="text-right">{translate("fixed")}</TableHead>
                {editable ? <TableHead className="sr-only">{translate("remove")}</TableHead> : null}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 ? (
                <TableRow>
                  <TableCell className="text-muted-foreground" colSpan={editable ? 5 : 4}>
                    {translate("empty")}
                  </TableCell>
                </TableRow>
              ) : (
                items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell className="font-mono text-xs">{item.provider}</TableCell>
                    <TableCell className="whitespace-nowrap font-mono text-xs">
                      {item.effectiveFrom}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(item.percentBp / 10000, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {item.fixedMinor > 0 && item.fixedCurrency
                        ? formatMoney(item.fixedMinor, item.fixedCurrency, locale)
                        : "—"}
                    </TableCell>
                    {editable ? (
                      <TableCell className="text-right">
                        <Button
                          aria-label={translate("remove")}
                          disabled={pending}
                          onClick={() => void remove(item.id)}
                          size="icon"
                          variant="ghost"
                        >
                          <Trash2 aria-hidden />
                        </Button>
                      </TableCell>
                    ) : null}
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

function percent(value: number | undefined, locale: string): string {
  if (value === undefined) {
    return "—";
  }
  return value.toLocaleString(locale, { style: "percent", maximumFractionDigits: 2 });
}

function gibibytes(bytes: number, locale: string): string {
  return `${(bytes / 2 ** 30).toLocaleString(locale, { maximumFractionDigits: 1 })} GiB`;
}

function moneyList(lines: PanelMoney[], locale: string): string {
  if (lines.length === 0) {
    return "—";
  }
  return lines.map((line) => formatMoney(line.amountMinor, line.currency, locale)).join(" · ");
}
