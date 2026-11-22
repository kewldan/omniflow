"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { InfoTip } from "@omniflow/ui/info-tip";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";

type Row = {
  id: string;
  status: string;
  telegramId?: number;
  balanceMinor: number;
  endsAt?: string;
  lastActivityAt?: string;
  createdAt: string;
};

const SORTS = ["newest", "oldest", "balance", "expiry", "activity"] as const;
const SWITCHES = ["withBalance", "autoRenew", "restricted", "trialOnly"] as const;

/**
 * The customer list's filtered and sorted views: expiring soon, with a
 * balance, renewing automatically, restricted, trial only, from a campaign,
 * active lately, or near their traffic limit — sorted by balance, expiry, or
 * last activity. A sorted view answers "the first 200 by this measure".
 */
export function AdvancedCustomerList() {
  const translate = useTranslations("admin.customers.advanced");
  const ids = {
    sort: useId(),
    expiring: useId(),
    campaign: useId(),
    active: useId(),
    traffic: useId(),
  };
  const [sort, setSort] = useState<string>("newest");
  const [expiring, setExpiring] = useState("");
  const [campaign, setCampaign] = useState("");
  const [activeDays, setActiveDays] = useState("");
  const [traffic, setTraffic] = useState("");
  const [flags, setFlags] = useState<Record<string, boolean>>({});
  const [query, setQuery] = useState<string | null>(null);
  const { data, isLoading } = useSWR<{ items: Row[] }, ApiError>(query, fetcher);

  function search() {
    const params = new URLSearchParams({ sort });
    if (expiring) params.set("expiringDays", expiring);
    if (campaign.trim()) params.set("campaign", campaign.trim());
    if (activeDays) params.set("activeWithinDays", activeDays);
    if (traffic) params.set("trafficPercent", traffic);
    for (const name of SWITCHES) {
      if (flags[name]) params.set(name, "true");
    }
    setQuery(`/v1/panel/customers/advanced?${params.toString()}`);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-4 rounded-md border border-dashed bg-well p-4">
          <div className="flex flex-wrap gap-x-6 gap-y-3">
            {SWITCHES.map((name) => (
              <div className="flex items-center gap-2" key={name}>
                <Switch
                  checked={Boolean(flags[name])}
                  id={`${ids.sort}-${name}`}
                  onCheckedChange={(on) => setFlags({ ...flags, [name]: on })}
                />
                <Label htmlFor={`${ids.sort}-${name}`}>{translate(name)}</Label>
                {translate.has(`hints.${name}`) && (
                  <InfoTip>{translate(`hints.${name}` as never)}</InfoTip>
                )}
              </div>
            ))}
          </div>
          <div className="grid items-end gap-3 sm:grid-cols-2 lg:grid-cols-5">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={ids.sort}>{translate("sort")}</Label>
              <Select onValueChange={setSort} value={sort}>
                <SelectTrigger id={ids.sort}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SORTS.map((value) => (
                    <SelectItem key={value} value={value}>
                      {translate(`sorts.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {[
              [ids.expiring, "expiringDays", expiring, setExpiring],
              [ids.active, "activeWithinDays", activeDays, setActiveDays],
              [ids.traffic, "trafficPercent", traffic, setTraffic],
            ].map(([id, key, value, set]) => (
              <div className="flex flex-col gap-1.5" key={key as string}>
                <span className="flex items-center gap-1.5">
                  <Label className="truncate" htmlFor={id as string}>
                    {translate(key as string)}
                  </Label>
                  <InfoTip>{translate(`hints.${key as string}` as never)}</InfoTip>
                </span>
                <Input
                  id={id as string}
                  inputMode="numeric"
                  onChange={(event) => (set as (next: string) => void)(event.target.value)}
                  value={value as string}
                />
              </div>
            ))}
            <div className="flex flex-col gap-1.5">
              <span className="flex items-center gap-1.5">
                <Label className="truncate" htmlFor={ids.campaign}>
                  {translate("campaign")}
                </Label>
                <InfoTip>{translate("hints.campaign")}</InfoTip>
              </span>
              <Input
                id={ids.campaign}
                onChange={(event) => setCampaign(event.target.value)}
                value={campaign}
              />
            </div>
          </div>
          <Button className="self-start" disabled={isLoading} onClick={search} size="sm">
            {translate("search")}
          </Button>
        </div>
        {data && data.items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {data && data.items.length > 0 ? (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("customer")}</TableHead>
                  <TableHead>{translate("balance")}</TableHead>
                  <TableHead>{translate("endsAt")}</TableHead>
                  <TableHead>{translate("lastActivity")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.items.map((row) => (
                  <TableRow key={row.id}>
                    <TableCell>
                      <CustomerChip id={row.id} />
                    </TableCell>
                    <TableCell className="tabular-nums">
                      {(row.balanceMinor / 100).toFixed(2)}
                    </TableCell>
                    <TableCell>{row.endsAt ? <Timestamp value={row.endsAt} /> : "—"}</TableCell>
                    <TableCell>
                      {row.lastActivityAt ? <Timestamp value={row.lastActivityAt} /> : "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
