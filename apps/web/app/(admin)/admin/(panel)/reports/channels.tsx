"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Download } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { ChannelResult } from "@/lib/operations";
import { formatMoney } from "@/lib/operations";

/**
 * Where the period's revenue came from, and the file that tells the advertising
 * platform about it.
 *
 * The column worth understanding is **uploadable**. An order is attributed to a
 * channel as soon as it carried a UTM tag, but only an order carrying the
 * platform's own click identifier can be matched back to the click that was
 * paid for. A campaign showing twenty orders and three uploadable ones is a
 * campaign whose links mostly lost their identifier somewhere — usually a
 * redirect that stripped the query — and that is worth knowing before anybody
 * concludes the advertising is not working.
 *
 * Nothing is uploaded from here. The export is a download, and what happens to
 * it next is the operator's decision.
 */
export function ChannelReport({ query }: { query: string }) {
  const translate = useTranslations("admin.reports.channels");
  const locale = useLocale();
  const { data, error, isLoading } = useSWR<{ channels: ChannelResult[] }, ApiError>(
    `/v1/panel/reports/channels${query}`,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (error) {
    return <StateNotice title={translate("failed")} variant="danger" />;
  }

  const channels = data?.channels ?? [];
  if (channels.length === 0) {
    return (
      <StateNotice
        description={translate("emptyAbout")}
        title={translate("empty")}
        variant="empty"
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-subtle-foreground text-sm">{translate("about")}</p>
        <Button asChild variant="secondary">
          {/* An ordinary link: the response is a file, and the browser's own
              download is what an operator expects. */}
          <a href={`/v1/panel/reports/conversions${query}${query ? "&" : "?"}format=csv`}>
            <Download aria-hidden />
            {translate("export")}
          </a>
        </Button>
      </div>

      <Card className="overflow-x-auto p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("column.channel")}</TableHead>
              <TableHead>{translate("column.medium")}</TableHead>
              <TableHead className="text-right">{translate("column.orders")}</TableHead>
              <TableHead className="text-right">{translate("column.uploadable")}</TableHead>
              <TableHead className="text-right">{translate("column.revenue")}</TableHead>
              <TableHead className="text-right">{translate("column.refunded")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {channels.map((channel) => (
              <TableRow key={`${channel.channel}-${channel.medium}-${channel.currency}`}>
                <TableCell>{channel.channel}</TableCell>
                <TableCell className="text-subtle-foreground">{channel.medium || "—"}</TableCell>
                <TableCell className="text-right tabular-nums">{channel.orders}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {channel.attributedClicks}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatMoney(channel.paidMinor, channel.currency, locale)}
                </TableCell>
                <TableCell className="text-right tabular-nums text-subtle-foreground">
                  {channel.refundedMinor
                    ? formatMoney(channel.refundedMinor, channel.currency, locale)
                    : "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}
