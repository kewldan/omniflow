"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Addon = {
  id: string;
  code: string;
  kind: string;
  visible: boolean;
  sortOrder: number;
  latestVersion: number;
  archivedAt?: string;
};

const KINDS = ["traffic", "devices", "squads"] as const;
const PRORATIONS = ["full_price", "remaining_period", "daily_rate"] as const;

/**
 * The add-on catalogue.
 *
 * An add-on splits the same way a plan does: the add-on row carries
 * presentation and is mutable, while the version carries what the customer gets
 * and what it costs, and is not. Saving publishes a new version, so a
 * historical order keeps costing what it cost.
 */
export function Addons({ active }: { active: boolean }) {
  const translate = useTranslations("admin.catalog.addons");
  const { can } = useSession();
  const [composing, setComposing] = useState(false);

  const { data, isLoading, mutate } = useSWR<Listing<Addon>, ApiError>(
    active ? "/v1/panel/catalog/addons" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];

  return (
    <div className="flex flex-col gap-4">
      {can("catalog.write") && (
        <Button className="self-start" onClick={() => setComposing((open) => !open)} size="sm">
          {translate(composing ? "close" : "create")}
        </Button>
      )}
      {composing && (
        <AddonComposer
          onSaved={() => {
            setComposing(false);
            void mutate();
          }}
        />
      )}
      {items.length === 0 ? (
        <StateNotice
          description={translate("empty.description")}
          title={translate("empty.title")}
          variant="empty"
        />
      ) : (
        <Card className="flex flex-col gap-2 p-4">
          {items.map((addon) => (
            <div className="flex flex-wrap items-center justify-between gap-3" key={addon.id}>
              <span className="flex items-center gap-2">
                <span className="font-mono text-[12px]">{addon.code}</span>
                <Badge variant="neutral">{translate(`kind.${addon.kind}`)}</Badge>
                <span className="text-muted-foreground text-xs">v{addon.latestVersion}</span>
              </span>
              <Badge variant={addon.visible ? "success" : "neutral"}>
                {translate(addon.visible ? "visible" : "hidden")}
              </Badge>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

/**
 * Creates an add-on and publishes a version of it.
 *
 * Proration is the interesting field. `full_price` charges the whole amount
 * whatever is left of the period, `remaining_period` scales it to what remains,
 * and `daily_rate` prices it by day — three answers to "what should a mid-period
 * purchase cost?", none of which is obviously right for every operator.
 */
function AddonComposer({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.catalog.addons");
  const codeId = useId();
  const kindId = useId();
  const nameEnId = useId();
  const nameRuId = useId();
  const payloadId = useId();
  const quantityId = useId();
  const prorationId = useId();
  const pricesId = useId();
  const visibleId = useId();
  const reasonId = useId();

  const [code, setCode] = useState("");
  const [kind, setKind] = useState<string>("traffic");
  const [nameEn, setNameEn] = useState("");
  const [nameRu, setNameRu] = useState("");
  const [payload, setPayload] = useState("");
  const [maxQuantity, setMaxQuantity] = useState("1");
  const [proration, setProration] = useState<string>("remaining_period");
  const [prices, setPrices] = useState("");
  const [visible, setVisible] = useState(false);
  const [reason, setReason] = useState("");

  const { run, pending, error } = useOperatorAction();
  const parsedPrices = parseAmounts(prices);
  const ready =
    code.trim().length > 0 &&
    nameEn.trim().length > 0 &&
    nameRu.trim().length > 0 &&
    payload.trim().length > 0 &&
    Object.keys(parsedPrices).length > 0 &&
    reason.trim().length > 0 &&
    !pending;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("composer.title")}</CardTitle>
        <CardDescription>{translate("composer.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <Row id={codeId} label={translate("composer.code")} onChange={setCode} value={code} />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={kindId}>{translate("composer.kind")}</Label>
            <select
              className="h-9 rounded-md border border-border bg-transparent px-2 text-sm"
              id={kindId}
              onChange={(event) => setKind(event.target.value)}
              value={kind}
            >
              {KINDS.map((option) => (
                <option key={option} value={option}>
                  {translate(`kind.${option}`)}
                </option>
              ))}
            </select>
          </div>
          <Row
            id={nameEnId}
            label={translate("composer.nameEn")}
            onChange={setNameEn}
            value={nameEn}
          />
          <Row
            id={nameRuId}
            label={translate("composer.nameRu")}
            onChange={setNameRu}
            value={nameRu}
          />
        </div>

        <Row
          hint={translate(`composer.payloadHint.${kind}`)}
          id={payloadId}
          label={translate(`composer.payload.${kind}`)}
          onChange={setPayload}
          value={payload}
        />

        <div className="grid gap-3 sm:grid-cols-2">
          <Row
            hint={translate("composer.quantityHint")}
            id={quantityId}
            label={translate("composer.quantity")}
            onChange={setMaxQuantity}
            value={maxQuantity}
          />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={prorationId}>{translate("composer.proration")}</Label>
            <select
              className="h-9 rounded-md border border-border bg-transparent px-2 text-sm"
              id={prorationId}
              onChange={(event) => setProration(event.target.value)}
              value={proration}
            >
              {PRORATIONS.map((option) => (
                <option key={option} value={option}>
                  {translate(`proration.${option}`)}
                </option>
              ))}
            </select>
          </div>
        </div>

        <Row
          hint={translate("composer.pricesHint")}
          id={pricesId}
          label={translate("composer.prices")}
          onChange={setPrices}
          value={prices}
        />
        <div className="flex items-center gap-3">
          <Switch checked={visible} id={visibleId} onCheckedChange={setVisible} />
          <Label htmlFor={visibleId}>{translate("composer.visible")}</Label>
        </div>
        <Row
          id={reasonId}
          label={translate("composer.reason")}
          onChange={setReason}
          value={reason}
        />

        <p className="text-muted-foreground text-xs">{translate("composer.immutability")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        <Button
          className="self-start"
          disabled={!ready}
          onClick={async () => {
            const ok = await run("/v1/panel/catalog/addons", {
              body: {
                code: code.trim().toLowerCase(),
                descriptionEn: "",
                descriptionRu: "",
                deviceSlots: kind === "devices" ? Number(payload) : null,
                kind,
                maxQuantity: Number(maxQuantity) || 1,
                nameEn: nameEn.trim(),
                nameRu: nameRu.trim(),
                prices: parsedPrices,
                proration,
                sortOrder: 0,
                // Traffic is entered in gigabytes and stored in bytes, because
                // every allowance in the product is bytes.
                squadIds:
                  kind === "squads"
                    ? payload
                        .split(",")
                        .map((value) => value.trim())
                        .filter(Boolean)
                    : [],
                trafficBytes:
                  kind === "traffic" ? Math.round(Number(payload) * 1_073_741_824) : null,
                visible,
              },
              method: "PUT",
              reason: reason.trim(),
            });
            if (ok) {
              setReason("");
              onSaved();
            }
          }}
          size="sm"
        >
          {translate("composer.submit")}
        </Button>
      </CardContent>
    </Card>
  );
}

function Row({
  hint,
  id,
  label,
  onChange,
  value,
}: {
  hint?: string;
  id: string;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} onChange={(event) => onChange(event.target.value)} value={value} />
      {hint && <span className="text-muted-foreground text-xs">{hint}</span>}
    </div>
  );
}

/** Parses `RUB=19900, USD=299` into minor-unit integers. */
function parseAmounts(input: string): Record<string, number> {
  const amounts: Record<string, number> = {};
  for (const entry of input.split(",")) {
    const [currency, amount] = entry.split("=");
    if (!currency || !amount) {
      continue;
    }
    const minor = Number(amount.trim());
    if (!Number.isInteger(minor) || minor < 0) {
      continue;
    }
    amounts[currency.trim().toUpperCase()] = minor;
  }
  return amounts;
}
