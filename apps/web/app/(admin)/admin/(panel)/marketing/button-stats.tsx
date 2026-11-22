"use client";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";

type Stat = { action: string; clicks: number; customers: number };

/**
 * Which bot screens, buttons, and commands customers use, over the last 7,
 * 30, or 90 days — counts only, with no customer in them.
 */
export function ButtonStats() {
  const translate = useTranslations("admin.buttonStats");
  const daysId = useId();
  const [days, setDays] = useState("7");
  const { data, isLoading } = useSWR<{ items: Stat[] }, ApiError>(
    `/v1/panel/reports/bot-buttons?days=${days}`,
    fetcher,
  );
  const items = data?.items ?? [];
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <label className="text-sm" htmlFor={daysId}>
            {translate("period")}
          </label>
          <Select onValueChange={setDays} value={days}>
            <SelectTrigger className="w-auto min-w-40" id={daysId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["7", "30", "90"].map((value) => (
                <SelectItem key={value} value={value}>
                  {translate("days", { count: Number(value) })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {!isLoading && items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {items.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("action")}</TableHead>
                <TableHead className="text-right">{translate("clicks")}</TableHead>
                <TableHead className="text-right">{translate("customers")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.action}>
                  <TableCell className="font-mono text-xs">{item.action}</TableCell>
                  <TableCell className="text-right tabular-nums">{item.clicks}</TableCell>
                  <TableCell className="text-right tabular-nums">{item.customers}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}
      </CardContent>
    </Card>
  );
}
