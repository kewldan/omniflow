"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney } from "@/lib/operations";

type Member = {
  customerId: string;
  level: number;
  via?: string;
  joinedAt: string;
  qualified: boolean;
  reviewState: string;
  earnedMinor: number;
  earnedDays: number;
};

type Level = {
  level: number;
  count: number;
  qualified: number;
  earnedMinor: number;
  members: Member[];
};

type Network = {
  customerId: string;
  currency: string;
  invitedBy?: string;
  invitedByUpline?: string;
  levels: Level[];
};

function CustomerLink({ id }: { id: string }) {
  return <CustomerChip id={id} />;
}

/**
 * The customer's referral network: who invited them and that inviter's
 * inviter above, and two levels of the people they brought below, with what
 * each earned them, in the referral programme's currency.
 */
export function ReferralNetworkPanel({
  active,
  customerId,
}: {
  active: boolean;
  customerId: string;
}) {
  const translate = useTranslations("admin.customers.referralNetwork");
  const locale = useLocale();
  const { data, error, isLoading } = useSWR<Network, ApiError>(
    active ? `/v1/panel/customers/${customerId}/referral-network` : null,
    fetcher,
  );

  return (
    <Card className="flex flex-col gap-4 p-4">
      <div className="font-medium">{translate("title")}</div>
      {isLoading && <Skeleton className="h-24 w-full" />}
      {error && <p className="text-destructive text-sm">{translate("error")}</p>}
      {data && <NetworkView data={data} locale={locale} />}
    </Card>
  );
}

function NetworkView({ data, locale }: { data: Network; locale: string }) {
  const translate = useTranslations("admin.customers.referralNetwork");
  const currency = data.currency;
  return (
    <>
      <p className="text-sm">
        {data.invitedBy ? (
          <>
            {translate("invitedBy")} <CustomerLink id={data.invitedBy} />
            {data.invitedByUpline && (
              <>
                {" "}
                ← <CustomerLink id={data.invitedByUpline} />
              </>
            )}
          </>
        ) : (
          <span className="text-muted-foreground">{translate("noInviter")}</span>
        )}
      </p>
      {data.levels.map((level) => (
        <section className="flex flex-col gap-2" key={level.level}>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="font-medium">{translate("level", { level: level.level })}</span>
            <Badge variant="neutral">
              {translate("counts", { count: level.count, qualified: level.qualified })}
            </Badge>
            <span className="text-muted-foreground">
              {translate("earned", { amount: formatMoney(level.earnedMinor, currency, locale) })}
            </span>
          </div>
          {level.members.length === 0 ? (
            <p className="text-muted-foreground text-sm">{translate("empty")}</p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("columns.customer")}</TableHead>
                    {level.level > 1 && <TableHead>{translate("columns.via")}</TableHead>}
                    <TableHead>{translate("columns.joined")}</TableHead>
                    <TableHead>{translate("columns.state")}</TableHead>
                    <TableHead className="text-right">{translate("columns.earned")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {level.members.map((member) => (
                    <TableRow key={member.customerId}>
                      <TableCell>
                        <CustomerLink id={member.customerId} />
                      </TableCell>
                      {level.level > 1 && (
                        <TableCell>{member.via && <CustomerLink id={member.via} />}</TableCell>
                      )}
                      <TableCell>
                        <Timestamp value={member.joinedAt} />
                      </TableCell>
                      <TableCell>
                        {member.reviewState === "rejected"
                          ? translate("states.rejected")
                          : member.qualified
                            ? translate("states.qualified")
                            : translate("states.pending")}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatMoney(member.earnedMinor, currency, locale)}
                        {member.earnedDays > 0 &&
                          ` · ${translate("days", { count: member.earnedDays })}`}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
          {level.members.length < level.count && (
            <p className="text-muted-foreground text-xs">
              {translate("truncated", { shown: level.members.length, count: level.count })}
            </p>
          )}
        </section>
      ))}
    </>
  );
}
