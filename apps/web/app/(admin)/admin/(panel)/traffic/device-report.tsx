"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";

type DeviceReport = {
  reported: boolean;
  byPlatform: { platform: string; count: number; byApp?: { app: string; count: number }[] }[];
  totalDevices?: number;
  uniqueDevices?: number;
  averagePerUser?: number;
};

type SubscriptionRequests = { items: { requestAt: string; ip: string; userAgent: string }[] };

/**
 * The panel's own device census: platforms and the applications on them.
 * Read live from Remnawave and stored nowhere, like the rest of this screen.
 */
export function DeviceCensusCard() {
  const translate = useTranslations("admin.traffic.devices");
  const { data, error, isLoading } = useSWR<DeviceReport, ApiError>(
    "/v1/panel/reports/devices",
    fetcher,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>
          {data?.reported
            ? translate("summary", {
                average: (data.averagePerUser ?? 0).toFixed(1),
                total: data.totalDevices ?? 0,
              })
            : translate("description")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {error ? (
          <StateNotice description={error.message} title={translate("failed")} variant="danger" />
        ) : isLoading || !data ? (
          <Skeleton className="h-32 w-full" />
        ) : !data.reported || data.byPlatform.length === 0 ? (
          <StateNotice description={translate("noneHint")} title={translate("none")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("platform")}</TableHead>
                <TableHead className="text-right">{translate("count")}</TableHead>
                <TableHead>{translate("apps")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.byPlatform.map((line) => (
                <TableRow key={line.platform}>
                  <TableCell>{line.platform || "—"}</TableCell>
                  <TableCell className="text-right font-mono">{line.count}</TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {(line.byApp ?? []).map((app) => `${app.app} ${app.count}`).join(", ")}
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

/**
 * When, from where, and by which application one panel user's subscription
 * was fetched — the way to tell whether a link is being used somewhere it
 * should not be.
 */
export function SubscriptionRequestsCard() {
  const translate = useTranslations("admin.traffic.requests");
  const inputId = useId();
  const [draft, setDraft] = useState("");
  const [userId, setUserId] = useState<string | null>(null);
  const { data, error, isLoading } = useSWR<SubscriptionRequests, ApiError>(
    userId ? `/v1/panel/reports/remnawave-users/${userId}/subscription-requests` : null,
    fetcher,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <form
          className="flex flex-col gap-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            setUserId(draft.trim());
          }}
        >
          <Label htmlFor={inputId}>{translate("userId")}</Label>
          <div className="flex gap-2">
            <Input
              id={inputId}
              inputMode="numeric"
              onChange={(event) => setDraft(event.target.value.replace(/\D/g, ""))}
              value={draft}
            />
            <Button disabled={!draft} type="submit" variant="outline">
              {translate("show")}
            </Button>
          </div>
        </form>
        {!userId ? null : error ? (
          <StateNotice description={error.message} title={translate("failed")} variant="danger" />
        ) : isLoading || !data ? (
          <Skeleton className="h-24 w-full" />
        ) : data.items.length === 0 ? (
          <StateNotice description={translate("noneHint")} title={translate("none")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("when")}</TableHead>
                <TableHead>{translate("ip")}</TableHead>
                <TableHead>{translate("app")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.items.map((item) => (
                <TableRow key={`${item.requestAt}-${item.ip}`}>
                  <TableCell>
                    <Timestamp value={item.requestAt} />
                  </TableCell>
                  <TableCell className="font-mono text-xs">{item.ip}</TableCell>
                  <TableCell className="max-w-64 truncate text-xs">{item.userAgent}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
