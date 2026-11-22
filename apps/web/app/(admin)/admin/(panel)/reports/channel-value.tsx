"use client";

import type {
  PanelChannelValueLine,
  PanelLocaleFunnel,
  PanelReferralProgramme,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";

import { StateNotice } from "@/components/admin/state-notice";
import { formatMoney } from "@/lib/operations";

/**
 * What the customers each channel brought are worth against what they cost.
 *
 * The cohort is the customers who paid for the first time in the period, each
 * in one channel only — the first touch Omniflow knows of — so the lines add
 * up to every new payer. Lifetime value is what they paid so far, not a
 * forecast, so a recent period reads low and its payback reads long.
 */
export function ChannelValueCard({
  lines,
  locale,
}: {
  lines: PanelChannelValueLine[];
  locale: string;
}) {
  const translate = useTranslations("admin.acquisition.channelValue");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {lines.length === 0 ? (
          <StateNotice description={translate("empty")} title={translate("emptyTitle")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("channel")}</TableHead>
                <TableHead className="text-right">{translate("payers")}</TableHead>
                <TableHead className="text-right">{translate("ltv")}</TableHead>
                <TableHead className="text-right">{translate("monthly")}</TableHead>
                <TableHead className="text-right">{translate("cac")}</TableHead>
                <TableHead className="text-right">{translate("ratio")}</TableHead>
                <TableHead className="text-right">{translate("payback")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {lines.map((line) => (
                <TableRow key={`${line.channel}-${line.currency}`}>
                  <TableCell>
                    <ChannelName channel={line.channel} />{" "}
                    <Badge variant="neutral">{line.currency}</Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.payers}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {money(line.ltvMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {money(line.monthlyRevenueMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.spendMinor > 0
                      ? money(line.cacMinor, line.currency, locale)
                      : translate("noSpend")}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.ltvToCac === undefined
                      ? "—"
                      : `${line.ltvToCac.toLocaleString(locale, { maximumFractionDigits: 1 })}×`}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.paybackMonths === undefined
                      ? "—"
                      : translate("months", {
                          value: line.paybackMonths.toLocaleString(locale, {
                            maximumFractionDigits: 1,
                          }),
                        })}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function ChannelName({ channel }: { channel: string }) {
  const translate = useTranslations("admin.acquisition.channelValue.kinds");
  const [kind, ...rest] = channel.split(":");
  const name = rest.join(":");
  if (kind === "campaign" || kind === "utm") {
    return (
      <span>
        {translate(kind)} <span className="font-mono text-xs">{name}</span>
      </span>
    );
  }
  return <span>{translate(kind === "referral" ? "referral" : "organic")}</span>;
}

/** The referral programme as an acquisition channel: who it brought and what it gave. */
export function ReferralCard({
  programme,
  locale,
}: {
  programme: PanelReferralProgramme;
  locale: string;
}) {
  const translate = useTranslations("admin.acquisition.referrals");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5 overflow-x-auto">
        <dl className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-5">
          {(
            [
              ["invited", String(programme.invited)],
              ["qualified", String(programme.qualified)],
              ["rejected", String(programme.rejected)],
              ["payers", String(programme.payers)],
              ["conversion", percent(programme.conversionRate, locale)],
            ] as const
          ).map(([key, value]) => (
            <div className="flex flex-col rounded-md border p-3" key={key}>
              <dt className="text-muted-foreground text-xs">{translate(key)}</dt>
              <dd className="text-xl font-semibold tabular-nums">{value}</dd>
            </div>
          ))}
        </dl>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("role")}</TableHead>
              <TableHead className="text-right">{translate("rewards")}</TableHead>
              <TableHead className="text-right">{translate("amount")}</TableHead>
              <TableHead className="text-right">{translate("days")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {programme.rewards.length === 0 ? (
              <TableRow>
                <TableCell className="text-muted-foreground" colSpan={4}>
                  {translate("none")}
                </TableCell>
              </TableRow>
            ) : (
              programme.rewards.map((line) => (
                <TableRow key={`${line.role}-${line.currency}`}>
                  <TableCell>{translate(`roles.${line.role}`)}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.rewards}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.amountMinor > 0
                      ? formatMoney(line.amountMinor, line.currency, locale)
                      : "—"}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.days || "—"}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
        {programme.economics.length > 0 ? (
          <ul className="flex flex-col gap-1 text-sm">
            {programme.economics.map((line) => (
              <li className="tabular-nums" key={line.currency}>
                <Badge variant="neutral">{line.currency}</Badge>{" "}
                {translate("economics", {
                  revenue: formatMoney(line.revenueMinor, line.currency, locale),
                  spend: formatMoney(line.spendMinor, line.currency, locale),
                })}
                {line.cacMinor === undefined
                  ? ""
                  : ` · ${translate("cac")} ${formatMoney(line.cacMinor, line.currency, locale)}`}
                {line.roi === undefined ? "" : ` · ROI ${percent(line.roi, locale)}`}
              </li>
            ))}
          </ul>
        ) : null}
      </CardContent>
    </Card>
  );
}

/** The funnel's two ends by interface language. */
export function LocaleFunnelCard({
  lines,
  locale,
}: {
  lines: PanelLocaleFunnel[];
  locale: string;
}) {
  const translate = useTranslations("admin.acquisition.locales");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {lines.length === 0 ? (
          <StateNotice description={translate("empty")} title={translate("emptyTitle")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("language")}</TableHead>
                <TableHead className="text-right">{translate("signups")}</TableHead>
                <TableHead className="text-right">{translate("trial")}</TableHead>
                <TableHead className="text-right">{translate("paid")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {lines.map((line) => (
                <TableRow key={line.locale}>
                  <TableCell className="font-mono text-xs uppercase">{line.locale}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.signups}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.trialClaimed} · {percent(line.trialRate, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.paid} · {percent(line.paidRate, locale)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
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
