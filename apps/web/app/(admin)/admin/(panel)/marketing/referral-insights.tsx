"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, type Listing, useOperatorAction } from "@/lib/operations";

type TopReferrer = { customerId: string; invited: number; qualified: number; earnedMinor: number };
type LostReferral = {
  referredCustomerId: string;
  referrerCustomerId: string;
  attributedAt: string;
  firstPaidAt: string;
  reason: string;
};

/**
 * Top referrers over a period, and referral diagnostics: pairs whose friend
 * paid without the inviter being rewarded, with a button that credits what
 * they missed under the programme in force.
 */
export function ReferralInsightsCard({
  editable,
  currency,
}: {
  editable: boolean;
  currency: string;
}) {
  const translate = useTranslations("admin.marketing.referralInsights");
  const locale = useLocale();
  const [days, setDays] = useState("30");
  const { data: top, isLoading } = useSWR<Listing<TopReferrer>, ApiError>(
    `/v1/panel/referrals/top?days=${days}&pageSize=20`,
    fetcher,
  );
  const { data: lost, mutate } = useSWR<Listing<LostReferral>, ApiError>(
    "/v1/panel/referrals/lost?pageSize=50",
    fetcher,
  );
  const { run, pending } = useOperatorAction();

  async function credit(customerId: string) {
    if (
      await run(`/v1/panel/referrals/lost/${customerId}/credit`, {
        method: "POST",
        reason: "referral back-credit",
      })
    ) {
      toast.success(translate("credited"));
      void mutate();
    }
  }

  const link = (id: string) => <CustomerChip id={id} />;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-2">
          <span className="text-sm">{translate("period")}</span>
          <Select onValueChange={setDays} value={days}>
            <SelectTrigger aria-label={translate("period")} className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["7", "30", "90", "365"].map((value) => (
                <SelectItem key={value} value={value}>
                  {translate("days", { days: Number(value) })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (top?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("noTop")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.inviter")}</TableHead>
                  <TableHead className="text-right">{translate("columns.invited")}</TableHead>
                  <TableHead className="text-right">{translate("columns.qualified")}</TableHead>
                  <TableHead className="text-right">{translate("columns.earned")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(top?.items ?? []).map((entry) => (
                  <TableRow key={entry.customerId}>
                    <TableCell>{link(entry.customerId)}</TableCell>
                    <TableCell className="text-right">{entry.invited}</TableCell>
                    <TableCell className="text-right">{entry.qualified}</TableCell>
                    <TableCell className="text-right">
                      {formatMoney(entry.earnedMinor, currency, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        <div className="font-medium text-sm">{translate("lostTitle")}</div>
        {(lost?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("noLost")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.friend")}</TableHead>
                  <TableHead>{translate("columns.inviter")}</TableHead>
                  <TableHead>{translate("columns.attributed")}</TableHead>
                  <TableHead>{translate("columns.paid")}</TableHead>
                  <TableHead>{translate("columns.reason")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(lost?.items ?? []).map((entry) => (
                  <TableRow key={entry.referredCustomerId}>
                    <TableCell>{link(entry.referredCustomerId)}</TableCell>
                    <TableCell>{link(entry.referrerCustomerId)}</TableCell>
                    <TableCell>
                      <Timestamp value={entry.attributedAt} />
                    </TableCell>
                    <TableCell>
                      <Timestamp value={entry.firstPaidAt} />
                    </TableCell>
                    <TableCell className="text-xs">
                      {translate(`reasons.${entry.reason}`)}
                    </TableCell>
                    <TableCell className="text-right">
                      {editable && (
                        <Button
                          disabled={pending}
                          onClick={() => void credit(entry.referredCustomerId)}
                          size="sm"
                          variant="outline"
                        >
                          {translate("credit")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
