"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type AppError = {
  id: string;
  process: string;
  message: string;
  source?: string;
  fingerprint: string;
  occurrences: number;
  firstSeenAt: string;
  lastSeenAt: string;
  resolvedAt?: string;
};

/**
 * The application error log: every error the api, bot, and worker logged,
 * grouped by fingerprint so a failure that repeats is one row with a count.
 * The operator group hears that a new error appeared; the message itself is
 * only here, behind system.read.
 */
export function ErrorLogCard() {
  const translate = useTranslations("admin.system.errors");
  const { can } = useSession();
  const resolvedId = useId();
  const [showResolved, setShowResolved] = useState(false);
  const { data, error, isLoading, mutate } = useSWR<{ items: AppError[] }, ApiError>(
    can("system.read") ? `/v1/panel/system/errors?resolved=${showResolved}` : null,
    fetcher,
  );
  const { run, pending } = useOperatorAction();

  if (!can("system.read")) {
    return null;
  }

  async function resolve(id?: string) {
    const ok = await run(
      id ? `/v1/panel/system/errors/${id}/resolve` : "/v1/panel/system/errors/resolve-all",
      { method: "POST", reason: "error log" },
    );
    if (ok) {
      await mutate();
    }
  }

  const items = data?.items ?? [];
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-4">
          <div className="flex items-center gap-2">
            <Switch checked={showResolved} id={resolvedId} onCheckedChange={setShowResolved} />
            <Label htmlFor={resolvedId}>{translate("showResolved")}</Label>
          </div>
          {can("system.write") && items.some((item) => !item.resolvedAt) ? (
            <Button disabled={pending} onClick={() => resolve()} size="sm" variant="outline">
              {translate("resolveAll")}
            </Button>
          ) : null}
        </div>
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {translate("failed")}
          </p>
        ) : null}
        {!isLoading && !error && items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {items.length > 0 ? (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("process")}</TableHead>
                  <TableHead>{translate("message")}</TableHead>
                  <TableHead>{translate("count")}</TableHead>
                  <TableHead>{translate("lastSeen")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <Badge variant="outline">{item.process}</Badge>
                    </TableCell>
                    <TableCell className="max-w-xl break-words font-mono text-xs">
                      {item.message}
                      {item.source ? (
                        <span className="block text-muted-foreground">{item.source}</span>
                      ) : null}
                    </TableCell>
                    <TableCell>{item.occurrences}</TableCell>
                    <TableCell>
                      <Timestamp value={item.lastSeenAt} />
                    </TableCell>
                    <TableCell>
                      {item.resolvedAt ? (
                        <Badge variant="neutral">{translate("resolved")}</Badge>
                      ) : can("system.write") ? (
                        <Button
                          disabled={pending}
                          onClick={() => resolve(item.id)}
                          size="sm"
                          variant="ghost"
                        >
                          {translate("resolve")}
                        </Button>
                      ) : null}
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
