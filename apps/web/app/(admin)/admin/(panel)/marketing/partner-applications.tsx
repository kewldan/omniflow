"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";

type Application = {
  id: string;
  customerId: string;
  status: "pending" | "approved" | "rejected";
  source: "bot" | "web";
  channel: string;
  message: string;
  commissionBps?: number;
  firstPaymentCommissionBps?: number;
  decisionNote?: string;
  createdAt: string;
  invited: number;
  qualified: number;
};

const STATUSES = ["pending", "approved", "rejected", "all"] as const;

const toBps = (value: string) =>
  value.trim() === "" ? undefined : Math.round((Number(value.replace(",", ".")) || 0) * 100);
const percent = (bps?: number) => (bps === undefined || bps === null ? "—" : `${bps / 100}%`);

/**
 * Partner applications: customers who asked to promote the service. Approving
 * sets the customer's personal commission rate — the same rate the customer
 * card edits — so the partner earns it on every later commission.
 */
export function PartnerApplicationsCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.partners");
  const [status, setStatus] = useState<(typeof STATUSES)[number]>("pending");
  const statusId = useId();
  const { data, isLoading, mutate } = useSWR<{ items: Application[] }, ApiError>(
    `/v1/panel/marketing/partner-applications${status === "all" ? "" : `?status=${status}`}`,
    fetcher,
  );
  const [deciding, setDeciding] = useState<string | null>(null);
  const items = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-2">
          <Label htmlFor={statusId}>{translate("filter")}</Label>
          <Select
            onValueChange={(value) => setStatus(value as (typeof STATUSES)[number])}
            value={status}
          >
            <SelectTrigger className="w-44" id={statusId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STATUSES.map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`statuses.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.customer")}</TableHead>
                  <TableHead>{translate("columns.application")}</TableHead>
                  <TableHead className="text-right">{translate("columns.referrals")}</TableHead>
                  <TableHead>{translate("columns.status")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((application) => (
                  <TableRow key={application.id}>
                    <TableCell>
                      <CustomerChip id={application.customerId} />
                      <div className="text-muted-foreground text-xs">
                        <Timestamp value={application.createdAt} /> ·{" "}
                        {translate(`sources.${application.source}`)}
                      </div>
                    </TableCell>
                    <TableCell className="max-w-md whitespace-normal text-sm">
                      {application.channel && (
                        <div className="font-medium">{application.channel}</div>
                      )}
                      {application.message && (
                        <div className="text-muted-foreground">{application.message}</div>
                      )}
                      {!application.channel && !application.message && (
                        <span className="text-muted-foreground">{translate("noText")}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {application.qualified} / {application.invited}
                    </TableCell>
                    <TableCell>
                      <Badge variant={application.status === "approved" ? "success" : "outline"}>
                        {translate(`statuses.${application.status}`)}
                      </Badge>
                      {application.status === "approved" && (
                        <div className="text-muted-foreground text-xs">
                          {percent(application.commissionBps)} ·{" "}
                          {percent(application.firstPaymentCommissionBps)}
                        </div>
                      )}
                      {application.decisionNote && (
                        <div className="text-muted-foreground text-xs">
                          {application.decisionNote}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      {editable && application.status === "pending" && (
                        <Button
                          onClick={() =>
                            setDeciding(deciding === application.id ? null : application.id)
                          }
                          size="sm"
                          variant="outline"
                        >
                          {translate("decide")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        {deciding && (
          <DecisionForm
            applicationId={deciding}
            onDone={() => {
              setDeciding(null);
              void mutate();
            }}
          />
        )}
      </CardContent>
    </Card>
  );
}

function DecisionForm({ applicationId, onDone }: { applicationId: string; onDone: () => void }) {
  const translate = useTranslations("admin.marketing.partners");
  const ids = { rate: useId(), first: useId(), note: useId() };
  const [rate, setRate] = useState("");
  const [first, setFirst] = useState("");
  const [note, setNote] = useState("");
  const { run, pending, error } = useOperatorAction();

  async function decide(approve: boolean) {
    const ok = await run(`/v1/panel/marketing/partner-applications/${applicationId}/decision`, {
      body: {
        approve,
        commissionBps: approve ? toBps(rate) : undefined,
        firstPaymentCommissionBps: approve ? toBps(first) : undefined,
        note,
      },
      method: "POST",
      reason: approve ? "partner approved" : "partner rejected",
    });
    if (ok) {
      onDone();
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="grid gap-3 sm:grid-cols-[10rem_10rem_1fr]">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.rate}>{translate("rate")}</Label>
          <Input
            id={ids.rate}
            inputMode="decimal"
            onChange={(event) => setRate(event.target.value)}
            value={rate}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.first}>{translate("first")}</Label>
          <Input
            id={ids.first}
            inputMode="decimal"
            onChange={(event) => setFirst(event.target.value)}
            value={first}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.note}>{translate("note")}</Label>
          <Input
            id={ids.note}
            maxLength={400}
            onChange={(event) => setNote(event.target.value)}
            value={note}
          />
        </div>
      </div>
      <p className="text-muted-foreground text-xs">{translate("hint")}</p>
      <div className="flex gap-2">
        <Button disabled={pending || rate.trim() === ""} onClick={() => void decide(true)}>
          {translate("approve")}
        </Button>
        <Button disabled={pending} onClick={() => void decide(false)} variant="outline">
          {translate("reject")}
        </Button>
      </div>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error.status === 409 ? translate("decided") : translate("invalid")}
        </p>
      )}
    </div>
  );
}
