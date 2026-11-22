"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { FormField } from "@/components/forms/field";
import { type ApiError, fetcher } from "@/lib/api";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Entry = {
  squadId: string;
  nameRu: string;
  nameEn: string;
  countryCode: string;
  maxUsers: number | null;
  trialEligible: boolean;
  sortOrder: number;
  prices: Record<string, number>;
  liveCustomers: number;
};

type Row = {
  squadId: string;
  panelName?: string;
  panelMembers?: number;
  inPanel: boolean;
  catalogued: boolean;
  entry?: Entry;
  capacityFilled: boolean;
};

const KEY = "/v1/panel/catalog/servers";

/** A country code as its flag, from the two regional-indicator letters. */
function flag(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) {
    return "";
  }
  return String.fromCodePoint(...[...code].map((letter) => 127_397 + letter.charCodeAt(0)));
}

/**
 * The server catalogue: how Omniflow labels and limits the sale of each
 * Remnawave internal squad — a name, a flag, a price for choosing it, a
 * customer limit, and whether trials may use it — beside the panel's own
 * member count, read live. Nothing here changes a squad or a node in
 * Remnawave, which stays authoritative for them.
 */
export function ServerCatalog() {
  const translate = useTranslations("admin.catalog.servers");
  const { data, mutate } = useSWR<{ reported: boolean; items: Row[] }, ApiError>(KEY, fetcher);
  const [editing, setEditing] = useState<string | null>(null);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {!data && <Skeleton className="h-32 w-full" />}
        {data && !data.reported && (
          <p className="text-muted-foreground text-xs">{translate("panelUnavailable")}</p>
        )}
        {data && data.items.length === 0 && (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        )}
        {data?.items.map((row) => (
          <div
            className="flex flex-col gap-2 rounded-lg border bg-well border-border p-3"
            key={row.squadId}
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="min-w-0">
                <p className="font-medium text-sm">
                  {row.entry
                    ? `${flag(row.entry.countryCode)} ${row.entry.nameEn}`.trim()
                    : row.panelName}
                </p>
                <p className="font-mono text-[11px] text-muted-foreground">
                  {row.panelName ?? row.squadId}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2 text-xs">
                {row.panelMembers !== undefined && (
                  <Badge variant="outline">
                    {translate("members", { count: row.panelMembers })}
                  </Badge>
                )}
                {row.entry && (
                  <Badge variant="outline">
                    {row.entry.maxUsers
                      ? translate("customersOf", {
                          count: row.entry.liveCustomers,
                          max: row.entry.maxUsers,
                        })
                      : translate("customers", { count: row.entry.liveCustomers })}
                  </Badge>
                )}
                {row.capacityFilled && <Badge variant="danger">{translate("full")}</Badge>}
                {row.entry && !row.entry.trialEligible && (
                  <Badge variant="neutral">{translate("noTrials")}</Badge>
                )}
                {!row.inPanel && data.reported && (
                  <Badge variant="danger">{translate("notInPanel")}</Badge>
                )}
                <Button
                  onClick={() => setEditing(editing === row.squadId ? null : row.squadId)}
                  size="sm"
                  variant="outline"
                >
                  {translate(editing === row.squadId ? "close" : row.catalogued ? "edit" : "add")}
                </Button>
              </div>
            </div>
            {editing === row.squadId && (
              <ServerForm
                onDone={() => {
                  setEditing(null);
                  void mutate();
                }}
                row={row}
              />
            )}
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function ServerForm({ onDone, row }: { onDone: () => void; row: Row }) {
  const translate = useTranslations("admin.catalog.servers");
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();
  const trialId = useId();
  const entry = row.entry;
  const [nameRu, setNameRu] = useState(entry?.nameRu ?? row.panelName ?? "");
  const [nameEn, setNameEn] = useState(entry?.nameEn ?? row.panelName ?? "");
  const [country, setCountry] = useState(entry?.countryCode ?? "");
  const [maxUsers, setMaxUsers] = useState(entry?.maxUsers ? String(entry.maxUsers) : "");
  const [trialEligible, setTrialEligible] = useState(entry?.trialEligible ?? true);
  const [priceCurrency, setPriceCurrency] = useState(Object.keys(entry?.prices ?? {})[0] ?? "RUB");
  const [price, setPrice] = useState(() => {
    const amount = entry?.prices?.[priceCurrency];
    return amount ? minorToInput(amount, priceCurrency) : "";
  });
  const [reason, setReason] = useState("");
  const writable = can("catalog.write");
  const path = `${KEY}/${row.squadId}`;

  async function save() {
    const currency = priceCurrency.trim().toUpperCase();
    const parsed = parseMajorToMinor(price, currency);
    const prices: Record<string, number> = {};
    if (price.trim() !== "" && parsed.ok && parsed.minor > 0) {
      prices[currency] = parsed.minor;
    }
    const ok = await run(path, {
      body: {
        countryCode: country.trim().toUpperCase(),
        maxUsers: maxUsers.trim() === "" ? null : Number(maxUsers.trim()),
        nameEn: nameEn.trim(),
        nameRu: nameRu.trim(),
        prices,
        sortOrder: entry?.sortOrder ?? 0,
        trialEligible,
      },
      method: "PUT",
      reason: reason.trim(),
    });
    if (ok) {
      onDone();
    }
  }

  async function remove() {
    if (await run(path, { method: "DELETE", reason: reason.trim() })) {
      onDone();
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-2 sm:grid-cols-3">
        <FormField label={translate("nameRu")}>
          {(props) => (
            <Input {...props} onChange={(event) => setNameRu(event.target.value)} value={nameRu} />
          )}
        </FormField>
        <FormField label={translate("nameEn")}>
          {(props) => (
            <Input {...props} onChange={(event) => setNameEn(event.target.value)} value={nameEn} />
          )}
        </FormField>
        <FormField hint={translate("countryHint")} label={translate("country")}>
          {(props) => (
            <Input
              {...props}
              maxLength={2}
              onChange={(event) => setCountry(event.target.value.toUpperCase())}
              value={country}
            />
          )}
        </FormField>
        <FormField hint={translate("maxUsersHint")} label={translate("maxUsers")}>
          {(props) => (
            <Input
              {...props}
              inputMode="numeric"
              onChange={(event) => setMaxUsers(event.target.value)}
              value={maxUsers}
            />
          )}
        </FormField>
        <FormField label={translate("currency")}>
          {(props) => (
            <Input
              {...props}
              maxLength={3}
              onChange={(event) => setPriceCurrency(event.target.value.toUpperCase())}
              value={priceCurrency}
            />
          )}
        </FormField>
        <FormField hint={translate("priceHint")} label={translate("price")}>
          {(props) => (
            <Input
              {...props}
              inputMode="decimal"
              onChange={(event) => setPrice(event.target.value)}
              value={price}
            />
          )}
        </FormField>
      </div>
      <div className="flex items-center gap-2">
        <Checkbox
          checked={trialEligible}
          id={trialId}
          onCheckedChange={(state) => setTrialEligible(state === true)}
        />
        <Label htmlFor={trialId}>{translate("trialEligible")}</Label>
      </div>
      {writable && (
        <>
          <FormField label={translate("reason")}>
            {(props) => (
              <Input
                {...props}
                onChange={(event) => setReason(event.target.value)}
                value={reason}
              />
            )}
          </FormField>
          {error && (
            <p className="text-danger-foreground text-sm" role="alert">
              {error.message}
            </p>
          )}
          <div className="flex gap-2">
            <Button disabled={pending} onClick={save} size="sm">
              {translate("save")}
            </Button>
            {row.catalogued && (
              <Button disabled={pending} onClick={remove} size="sm" variant="ghost">
                {translate("remove")}
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  );
}
