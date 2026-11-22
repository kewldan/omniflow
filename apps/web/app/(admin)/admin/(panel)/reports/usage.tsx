"use client";

import type { PanelUsageReport } from "@omniflow/api-client/generated";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";

/**
 * Whether customers use what they bought.
 *
 * Built from the worker's hourly census of Remnawave users, which records when
 * each first connected, when they were last online, the node they last used,
 * and their devices against the limit. A trial that never connected almost
 * never converts, and a payer who stopped connecting is the earliest sign of a
 * customer about to leave — both are invisible to every money report.
 */
export function UsageReportScreen({ query }: { query: string }) {
  const translate = useTranslations("admin.usage");
  const locale = useLocale();
  const { data, error, isLoading } = useSWR<PanelUsageReport, ApiError>(
    `/v1/panel/reports/usage${query}`,
    fetcher,
  );

  if (error) {
    return <StateNotice description={error.message} title={translate("failed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-96 w-full" />;
  }
  if (!data.observedAt) {
    return (
      <StateNotice description={translate("pendingDescription")} title={translate("pending")} />
    );
  }

  const { trials, purchases, paying } = data;

  return (
    <>
      <p className="text-muted-foreground text-xs">
        {translate("observedAt", {
          moment: new Date(data.observedAt).toLocaleString(locale, {
            dateStyle: "medium",
            timeStyle: "short",
          }),
        })}
      </p>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          hint={translate("tiles.trialConnectedHint", { count: trials.trials })}
          label={translate("tiles.trialConnected")}
          value={percent(trials.connectionRate, locale)}
        />
        <Tile
          hint={translate("tiles.medianHint")}
          label={translate("tiles.median")}
          value={
            trials.medianHoursToConnect === undefined
              ? "—"
              : translate("hours", {
                  value: trials.medianHoursToConnect.toLocaleString(locale, {
                    maximumFractionDigits: 1,
                  }),
                })
          }
        />
        <Tile
          hint={translate("tiles.purchaseConnectedHint", { count: purchases.payers })}
          label={translate("tiles.purchaseConnected")}
          value={percent(purchases.connectionRate, locale)}
        />
        <Tile
          hint={translate("tiles.activeHint", { count: paying.observed })}
          label={translate("tiles.active")}
          value={percent(paying.activeRate, locale)}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{translate("trials.title")}</CardTitle>
          <CardDescription>{translate("trials.description")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Figure
            detail={translate("trials.ofConnected", {
              paid: trials.convertedConnected,
              total: trials.connected,
            })}
            label={translate("trials.connectedConversion")}
            value={percent(trials.connectedConversionRate, locale)}
          />
          <Figure
            detail={translate("trials.ofUnconnected", {
              paid: trials.convertedUnconnected,
              total: trials.trials - trials.connected,
            })}
            label={translate("trials.unconnectedConversion")}
            value={percent(trials.unconnectedConversionRate, locale)}
          />
          <Figure
            detail={translate("trials.withinDayDetail", { count: trials.connectedWithinDay })}
            label={translate("trials.withinDay")}
            value={percent(
              trials.trials > 0 ? trials.connectedWithinDay / trials.trials : undefined,
              locale,
            )}
          />
          <Figure
            detail={translate("trials.purchasesDetail", {
              connected: purchases.connected,
              total: purchases.payers,
            })}
            label={translate("trials.purchasesWithinDay")}
            value={percent(
              purchases.payers > 0 ? purchases.connectedWithinDay / purchases.payers : undefined,
              locale,
            )}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("paying.title")}</CardTitle>
          <CardDescription>{translate("paying.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
            {(
              [
                ["customers", paying.customers],
                ["observed", paying.observed],
                ["onlineWeek", paying.onlineWeek],
                ["onlineMonth", paying.onlineMonth],
                ["neverConnected", paying.neverConnected],
                ["atDeviceLimit", paying.atDeviceLimit],
                ["overDeviceLimit", paying.overDeviceLimit],
              ] as const
            ).map(([key, value]) => (
              <div className="flex flex-col rounded-md border p-3" key={key}>
                <dt className="text-muted-foreground text-xs">{translate(`paying.${key}`)}</dt>
                <dd className="text-xl font-semibold tabular-nums">{value}</dd>
              </div>
            ))}
          </dl>
          <p className="text-muted-foreground mt-3 text-xs">{translate("paying.deviceHint")}</p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("nodes.title")}</CardTitle>
          <CardDescription>{translate("nodes.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {data.nodes.length === 0 ? (
            <StateNotice description={translate("nodes.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("nodes.node")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.paying")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.ended")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.renewal")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.tickets")}</TableHead>
                  <TableHead className="text-right">{translate("nodes.perHundred")}</TableHead>
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
                      {node.payingCustomers}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{node.periodsEnded}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(node.renewalRate, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{node.tickets}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {node.ticketsPerHundred === undefined
                        ? "—"
                        : node.ticketsPerHundred.toLocaleString(locale, {
                            maximumFractionDigits: 1,
                          })}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </>
  );
}

function Tile({ hint, label, value }: { hint: string; label: string; value: string }) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription>{label}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      <CardContent className="text-muted-foreground text-xs">{hint}</CardContent>
    </Card>
  );
}

function Figure({ detail, label, value }: { detail: string; label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border p-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="text-xl font-semibold tabular-nums">{value}</span>
      <span className="text-muted-foreground text-xs">{detail}</span>
    </div>
  );
}

function percent(value: number | undefined, locale: string): string {
  if (value === undefined) {
    return "—";
  }
  return value.toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 });
}
