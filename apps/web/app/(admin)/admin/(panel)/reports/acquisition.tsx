"use client";

import type {
  PanelAcquisitionEconomics,
  PanelAcquisitionReport,
  PanelAcquisitionSpend,
  PanelFunnel,
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

import { ChannelValueCard, LocaleFunnelCard, ReferralCard } from "./channel-value";

type AdCampaignOption = { id: string; code: string; name: string };

/**
 * What acquiring a customer cost and what it brought.
 *
 * Omniflow sees the revenue an advertisement produced and never its invoice,
 * so costs are entered here by hand, against a /start campaign or a
 * `utm_source`. CAC and ROI appear only where cost and revenue share a
 * currency: nothing is converted.
 */
export function AcquisitionReportScreen({ query }: { query: string }) {
  const translate = useTranslations("admin.acquisition");
  const locale = useLocale();
  const { data, error, isLoading, mutate } = useSWR<PanelAcquisitionReport, ApiError>(
    `/v1/panel/reports/acquisition${query}`,
    fetcher,
  );

  if (error) {
    return <StateNotice description={error.message} title={translate("failed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-96 w-full" />;
  }

  return (
    <>
      <FunnelCard funnel={data.funnel} locale={locale} />
      <ChannelValueCard lines={data.channelValue} locale={locale} />
      <ReferralCard locale={locale} programme={data.referrals} />
      <LocaleFunnelCard lines={data.byLocale} locale={locale} />

      <Card>
        <CardHeader>
          <CardTitle>{translate("campaigns.title")}</CardTitle>
          <CardDescription>{translate("campaigns.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {data.campaigns.length === 0 ? (
            <StateNotice description={translate("campaigns.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("campaigns.campaign")}</TableHead>
                  <TableHead className="text-right">{translate("campaigns.signups")}</TableHead>
                  <TableHead className="text-right">{translate("campaigns.payers")}</TableHead>
                  <TableHead className="text-right">{translate("campaigns.conversion")}</TableHead>
                  <TableHead>{translate("economics.title")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.campaigns.map((line) => (
                  <TableRow key={line.campaignId}>
                    <TableCell>
                      <div className="font-medium">{line.name}</div>
                      <div className="text-muted-foreground font-mono text-xs">{line.code}</div>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{line.signups}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.payers}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(line.conversionRate, locale)}
                    </TableCell>
                    <TableCell>
                      <EconomicsList lines={line.economics} locale={locale} />
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
          <CardTitle>{translate("sources.title")}</CardTitle>
          <CardDescription>{translate("sources.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {data.sources.length === 0 ? (
            <StateNotice description={translate("sources.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("sources.source")}</TableHead>
                  <TableHead className="text-right">{translate("sources.orders")}</TableHead>
                  <TableHead className="text-right">{translate("sources.newPayers")}</TableHead>
                  <TableHead className="text-right">{translate("economics.revenue")}</TableHead>
                  <TableHead className="text-right">{translate("economics.spend")}</TableHead>
                  <TableHead className="text-right">{translate("economics.cac")}</TableHead>
                  <TableHead className="text-right">{translate("economics.roi")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.sources.map((line) => (
                  <TableRow key={`${line.utmSource}-${line.currency}`}>
                    <TableCell className="font-mono text-xs">
                      {line.utmSource} <Badge variant="neutral">{line.currency}</Badge>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{line.orders}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.newPayers}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.revenueMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.spendMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {money(line.cacMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(line.roi, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <SpendCard onChange={() => void mutate()} query={query} />

      <Card>
        <CardHeader>
          <CardTitle>{translate("promo.title")}</CardTitle>
          <CardDescription>{translate("promo.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {data.promoCodes.length === 0 ? (
            <StateNotice description={translate("promo.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("promo.code")}</TableHead>
                  <TableHead className="text-right">{translate("promo.redemptions")}</TableHead>
                  <TableHead className="text-right">{translate("promo.newCustomers")}</TableHead>
                  <TableHead className="text-right">{translate("promo.discount")}</TableHead>
                  <TableHead className="text-right">{translate("economics.revenue")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.promoCodes.map((line) => (
                  <TableRow key={`${line.code}-${line.currency}`}>
                    <TableCell className="font-mono text-xs">
                      {line.code} <Badge variant="neutral">{line.promotionKind}</Badge>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{line.redemptions}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.newCustomers}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.discountMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.revenueMinor, line.currency, locale)}
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
          <CardTitle>{translate("given.title")}</CardTitle>
          <CardDescription>{translate("given.description")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-6 overflow-x-auto lg:grid-cols-2">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("given.creditSource")}</TableHead>
                <TableHead className="text-right">{translate("given.grants")}</TableHead>
                <TableHead className="text-right">{translate("given.amount")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.bonusCredit.length === 0 ? (
                <TableRow>
                  <TableCell className="text-muted-foreground" colSpan={3}>
                    {translate("given.none")}
                  </TableCell>
                </TableRow>
              ) : (
                data.bonusCredit.map((line) => (
                  <TableRow key={`${line.source}-${line.currency}`}>
                    <TableCell className="font-mono text-xs">{line.source}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.grants}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.amountMinor, line.currency, locale)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("given.accessSource")}</TableHead>
                <TableHead className="text-right">{translate("given.grants")}</TableHead>
                <TableHead className="text-right">{translate("given.days")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.freeAccess.length === 0 ? (
                <TableRow>
                  <TableCell className="text-muted-foreground" colSpan={3}>
                    {translate("given.none")}
                  </TableCell>
                </TableRow>
              ) : (
                data.freeAccess.map((line) => (
                  <TableRow key={line.source}>
                    <TableCell className="font-mono text-xs">{line.source}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.grants}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.days}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  );
}

/**
 * Of the customers who signed up in the period, how many reached each step.
 * A cohort measure: it reads low for a period that ends today.
 */
function FunnelCard({ funnel, locale }: { funnel: PanelFunnel; locale: string }) {
  const translate = useTranslations("admin.acquisition.funnel");
  const steps = [
    { key: "signups", value: funnel.signups },
    { key: "plansViewed", value: funnel.plansViewed },
    { key: "checkoutStarted", value: funnel.checkoutStarted },
    { key: "trialClaimed", value: funnel.trialClaimed },
    { key: "paid", value: funnel.paid },
  ] as const;
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {steps.map((step) => {
          const share = funnel.signups > 0 ? step.value / funnel.signups : undefined;
          return (
            <div className="flex flex-col gap-1" key={step.key}>
              <div className="flex justify-between text-sm">
                <span>{translate(step.key)}</span>
                <span className="tabular-nums">
                  {step.value}
                  {share === undefined || step.key === "signups"
                    ? ""
                    : ` · ${percent(share, locale)}`}
                </span>
              </div>
              <div aria-hidden className="bg-muted h-2 overflow-hidden rounded-full">
                <div
                  className="bg-primary h-full rounded-full"
                  style={{ width: `${Math.round((share ?? 0) * 100)}%` }}
                />
              </div>
            </div>
          );
        })}
        {funnel.byChannel.length > 0 ? (
          <div className="text-muted-foreground flex flex-wrap gap-2 text-xs">
            {funnel.byChannel.map((line) => (
              <Badge key={`${line.step}-${line.channel}`} variant="neutral">
                {translate(line.step === "plans_viewed" ? "plansViewed" : "checkoutStarted")} ·{" "}
                {translate(`channels.${line.channel}`)} · {line.customers}
              </Badge>
            ))}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function EconomicsList({ lines, locale }: { lines: PanelAcquisitionEconomics[]; locale: string }) {
  const translate = useTranslations("admin.acquisition.economics");
  if (lines.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <ul className="flex flex-col gap-1 text-sm">
      {lines.map((line) => (
        <li className="tabular-nums" key={line.currency}>
          <Badge variant="neutral">{line.currency}</Badge>{" "}
          {translate("line", {
            revenue: formatMoney(line.revenueMinor, line.currency, locale),
            spend: formatMoney(line.spendMinor, line.currency, locale),
          })}
          {line.cacMinor === undefined
            ? ""
            : ` · ${translate("cac")} ${formatMoney(line.cacMinor, line.currency, locale)}`}
          {line.roi === undefined ? "" : ` · ${translate("roi")} ${percent(line.roi, locale)}`}
        </li>
      ))}
    </ul>
  );
}

/**
 * The advertising costs recorded for the period, and the form that records
 * one. Reading needs finance.read; recording and removing need finance.write,
 * which the API enforces whatever this form shows.
 */
function SpendCard({ onChange, query }: { onChange: () => void; query: string }) {
  const translate = useTranslations("admin.acquisition.spend");
  const period = useTranslations("admin.reports.period");
  const locale = useLocale();
  const { can } = useSession();
  const editable = can("finance.write");
  const { data, mutate } = useSWR<{ items: PanelAcquisitionSpend[] }, ApiError>(
    `/v1/panel/reports/acquisition/spend${query}`,
    fetcher,
  );
  const { data: campaigns } = useSWR<{ items: AdCampaignOption[] }, ApiError>(
    editable && can("marketing.read") ? "/v1/panel/marketing/ad-campaigns" : null,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const submission = useSubmission();
  const ids = {
    target: useId(),
    campaign: useId(),
    source: useId(),
    day: useId(),
    currency: useId(),
    amount: useId(),
    note: useId(),
  };
  const [target, setTarget] = useState<"campaign" | "source">("source");
  const [campaignId, setCampaignId] = useState("");
  const [source, setSource] = useState("");
  const [day, setDay] = useState("");
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
    if (!day) {
      setProblem(translate("invalidDay"));
      return;
    }
    setProblem("");
    const ok = await run("/v1/panel/reports/acquisition/spend", {
      body: {
        campaignId: target === "campaign" ? campaignId : undefined,
        utmSource: target === "source" ? source.trim().toLowerCase() : undefined,
        spentOn: day.slice(0, 10),
        currency: currency.trim().toUpperCase(),
        amountMinor: parsed.minor,
        note: note.trim(),
      },
      method: "POST",
      reason: "advertising cost",
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
    const ok = await run(`/v1/panel/reports/acquisition/spend/${id}`, {
      method: "DELETE",
      reason: "advertising cost entered by mistake",
    });
    if (ok) {
      void mutate();
      onChange();
    }
  }

  const items = data?.items ?? [];

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
              <Label htmlFor={ids.target}>{translate("target")}</Label>
              <Select
                onValueChange={(value) => setTarget(value as "campaign" | "source")}
                value={target}
              >
                <SelectTrigger id={ids.target}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="source">{translate("targetSource")}</SelectItem>
                  <SelectItem value="campaign">{translate("targetCampaign")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {target === "campaign" ? (
              <div className="flex flex-col gap-2">
                <Label htmlFor={ids.campaign}>{translate("campaign")}</Label>
                <Select onValueChange={setCampaignId} value={campaignId}>
                  <SelectTrigger id={ids.campaign}>
                    <SelectValue placeholder={translate("campaignPlaceholder")} />
                  </SelectTrigger>
                  <SelectContent>
                    {(campaigns?.items ?? []).map((campaign) => (
                      <SelectItem key={campaign.id} value={campaign.id}>
                        {campaign.name} ({campaign.code})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            ) : (
              <div className="flex flex-col gap-2">
                <Label htmlFor={ids.source}>{translate("source")}</Label>
                <Input
                  id={ids.source}
                  onChange={(event) => setSource(event.target.value)}
                  placeholder="yandex"
                  required
                  value={source}
                />
              </div>
            )}
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.day}>{translate("day")}</Label>
              <DateTimeField
                hourLabel={period("hour")}
                id={ids.day}
                minuteLabel={period("minute")}
                onChange={setDay}
                placeholder={translate("day")}
                value={day}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.currency}>{translate("currency")}</Label>
              <Input
                id={ids.currency}
                maxLength={3}
                onChange={(event) => setCurrency(event.target.value.toUpperCase())}
                required
                value={currency}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={ids.amount}>{translate("amount")}</Label>
              <Input
                id={ids.amount}
                inputMode="decimal"
                onChange={(event) => setAmount(event.target.value)}
                required
                value={amount}
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
              <Button disabled={pending || (target === "campaign" && !campaignId)} type="submit">
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
                <TableHead>{translate("day")}</TableHead>
                <TableHead>{translate("target")}</TableHead>
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
                      {item.spentOn}
                    </TableCell>
                    <TableCell>
                      {item.campaignId ? (
                        <>
                          {item.campaignName}{" "}
                          <span className="text-muted-foreground font-mono text-xs">
                            {item.campaignCode}
                          </span>
                        </>
                      ) : (
                        <span className="font-mono text-xs">{item.utmSource}</span>
                      )}
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

function percent(value: number | undefined, locale: string): string {
  if (value === undefined) {
    return "—";
  }
  return value.toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 });
}

function money(minor: number | undefined, currency: string, locale: string): string {
  return minor === undefined ? "—" : formatMoney(minor, currency, locale);
}
