"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { useOperatorAction } from "@/lib/operations";

/** The segment vocabulary and how each value is entered; mirrors internal/audience. */
const FILTERS: Record<
  string,
  { kind: "choice" | "number" | "range" | "text" | "ids"; options?: string[] }
> = {
  status: { kind: "choice", options: ["active", "suspended", "deleted"] },
  locale: { kind: "choice", options: ["ru", "en"] },
  subscription: { kind: "choice", options: ["active", "expired", "none"] },
  trial: { kind: "choice", options: ["active", "used", "never"] },
  expiringWithinDays: { kind: "number" },
  expiredDaysAgo: { kind: "range" },
  inactiveForDays: { kind: "number" },
  activeWithinDays: { kind: "number" },
  registeredWithinDays: { kind: "number" },
  registeredBeforeDays: { kind: "number" },
  trafficUsedPercent: { kind: "number" },
  minimumSpendMinor: { kind: "number" },
  zeroBalance: { kind: "choice", options: ["true", "false"] },
  referrals: { kind: "choice", options: ["inviter", "invitee"] },
  signInMethod: { kind: "choice", options: ["telegram", "email", "vkid", "oidc"] },
  loyaltyTier: { kind: "text" },
  planCode: { kind: "text" },
  adCampaign: { kind: "text" },
  customerIds: { kind: "ids" },
};

/** `value` holds the single value, or the lower bound of a range; `upper` its upper bound. */
type Row = { key: string; filter: string; value: string; upper: string };

function encode(row: Row): unknown {
  const spec = FILTERS[row.filter];
  if (!spec) {
    return row.value;
  }
  if (spec.kind === "number") {
    return Number(row.value) || 0;
  }
  if (spec.kind === "range") {
    // An empty bound is left out rather than sent as zero: "at least 30 days
    // ago" and "between 0 and 30 days ago" are different audiences.
    const bounds: Record<string, number> = {};
    if (row.value.trim() !== "") {
      bounds.min = Number(row.value);
    }
    if (row.upper.trim() !== "") {
      bounds.max = Number(row.upper);
    }
    return bounds;
  }
  if (row.filter === "zeroBalance") {
    return row.value === "true";
  }
  if (spec.kind === "ids") {
    return row.value
      .split(/[\s,]+/)
      .map((id) => id.trim())
      .filter(Boolean);
  }
  return row.value.trim();
}

/**
 * Create or replace an audience segment from the closed filter vocabulary.
 * The API refuses a filter it cannot compile, and saving under an existing
 * code replaces that segment; its definition is shown in words in the list.
 */
export function SegmentEditorCard({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.marketing.segmentEditor");
  const ids = { code: useId(), ru: useId(), en: useId() };
  const [code, setCode] = useState("");
  const [nameRu, setNameRu] = useState("");
  const [nameEn, setNameEn] = useState("");
  const [rows, setRows] = useState<Row[]>([]);
  const { run, pending, error } = useOperatorAction();
  const used = new Set(rows.map((row) => row.filter));

  function setRow(index: number, patch: Partial<Row>) {
    setRows((all) => all.map((row, at) => (at === index ? { ...row, ...patch } : row)));
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const filters = Object.fromEntries(rows.map((row) => [row.filter, encode(row)]));
    const ok = await run("/v1/panel/marketing/segments", {
      method: "PUT",
      reason: "audience segment",
      body: {
        code: code.trim().toLowerCase(),
        nameRu: nameRu.trim(),
        nameEn: nameEn.trim(),
        filters,
      },
    });
    if (ok) {
      toast.success(translate("saved"));
      setCode("");
      setNameRu("");
      setNameEn("");
      setRows([]);
      onSaved();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-3" onSubmit={save}>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.code}>{translate("code")}</Label>
              <Input
                id={ids.code}
                onChange={(event) => setCode(event.target.value)}
                placeholder="lapsed_trials"
                value={code}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.ru}>{translate("nameRu")}</Label>
              <Input
                id={ids.ru}
                onChange={(event) => setNameRu(event.target.value)}
                value={nameRu}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.en}>{translate("nameEn")}</Label>
              <Input
                id={ids.en}
                onChange={(event) => setNameEn(event.target.value)}
                value={nameEn}
              />
            </div>
          </div>
          {rows.map((row, index) => {
            const spec = FILTERS[row.filter];
            return (
              <div className="grid gap-2 sm:grid-cols-[16rem_1fr_auto]" key={row.key}>
                <Select
                  onValueChange={(filter) => setRow(index, { filter, value: "", upper: "" })}
                  value={row.filter}
                >
                  <SelectTrigger aria-label={translate("filter")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.keys(FILTERS)
                      .filter((filter) => filter === row.filter || !used.has(filter))
                      .map((filter) => (
                        <SelectItem key={filter} value={filter}>
                          {translate(`filters.${filter}`)}
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
                {spec?.kind === "choice" ? (
                  <Select onValueChange={(value) => setRow(index, { value })} value={row.value}>
                    <SelectTrigger aria-label={translate("value")}>
                      <SelectValue placeholder={translate("pick")} />
                    </SelectTrigger>
                    <SelectContent>
                      {(spec.options ?? []).map((option) => (
                        <SelectItem key={option} value={option}>
                          {translate(`values.${option}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : spec?.kind === "range" ? (
                  <div className="grid grid-cols-2 gap-2">
                    <Input
                      aria-label={translate("rangeFrom")}
                      inputMode="numeric"
                      onChange={(event) => setRow(index, { value: event.target.value })}
                      placeholder={translate("rangeFrom")}
                      value={row.value}
                    />
                    <Input
                      aria-label={translate("rangeTo")}
                      inputMode="numeric"
                      onChange={(event) => setRow(index, { upper: event.target.value })}
                      placeholder={translate("rangeTo")}
                      value={row.upper}
                    />
                  </div>
                ) : spec?.kind === "ids" ? (
                  <Textarea
                    aria-label={translate("value")}
                    onChange={(event) => setRow(index, { value: event.target.value })}
                    placeholder={translate("idsHint")}
                    rows={2}
                    value={row.value}
                  />
                ) : (
                  <Input
                    aria-label={translate("value")}
                    inputMode={spec?.kind === "number" ? "numeric" : undefined}
                    onChange={(event) => setRow(index, { value: event.target.value })}
                    value={row.value}
                  />
                )}
                <Button
                  onClick={() => setRows((all) => all.filter((_, at) => at !== index))}
                  type="button"
                  variant="outline"
                >
                  {translate("remove")}
                </Button>
              </div>
            );
          })}
          <Button
            className="self-start"
            disabled={used.size === Object.keys(FILTERS).length}
            onClick={() =>
              setRows((all) => [
                ...all,
                {
                  key: crypto.randomUUID(),
                  filter: Object.keys(FILTERS).find((filter) => !used.has(filter)) ?? "status",
                  value: "",
                  upper: "",
                },
              ])
            }
            type="button"
            variant="outline"
          >
            {translate("addFilter")}
          </Button>
          <p className="text-muted-foreground text-xs">{translate("everybody")}</p>
          {error && (
            <p className="text-destructive text-sm" role="alert">
              {translate("invalid")}
            </p>
          )}
          <Button
            className="self-start"
            disabled={pending || !code.trim() || !nameRu.trim() || !nameEn.trim()}
            type="submit"
          >
            {translate("save")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
