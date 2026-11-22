"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { paymentMethodName, useEnumLabel } from "@/components/admin/enum-label";
import { type ApiError, fetcher } from "@/lib/api";
import { type CommerceSettings, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/** One customer-facing payment button as the panel configures it. */
type PaymentMethodSettings = {
  provider: string;
  method: string;
  key: string;
  configured: boolean;
  enabled: boolean;
  displayOrder: number;
  nameRu: string;
  nameEn: string;
  descriptionRu: string;
  descriptionEn: string;
  minMinor?: number;
  maxMinor?: number;
  quickAmountsMinor: number[];
  loginKinds: string[];
  topUpAudience: "all" | "first_topup" | "repeat_topup";
  loyaltyTierCodes: string[];
  openDirectly: boolean;
};

const LOGIN_KINDS = ["telegram", "email", "vk", "oidc"];

/**
 * Per-button presentation of the payment methods: what a customer sees on
 * each button, the amounts it accepts, and who is shown it. Amounts are in
 * minor units, as everywhere in the API.
 */
export function PaymentMethodsCard() {
  const translate = useTranslations("admin.settings.paymentMethods");
  const label = useEnumLabel();
  const { can } = useSession();
  const [editing, setEditing] = useState("");
  const { data, isLoading, mutate } = useSWR<{ items: PaymentMethodSettings[] }, ApiError>(
    "/v1/panel/settings/payment-methods",
    fetcher,
  );
  const items = [...(data?.items ?? [])].sort((left, right) =>
    left.provider === right.provider
      ? left.displayOrder - right.displayOrder
      : left.provider.localeCompare(right.provider),
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          items.map((item) => (
            <div className="rounded-md border bg-well border-border p-3" key={item.key}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[13px]">{paymentMethodName(label, item.key)}</span>
                <Badge variant={item.enabled ? "success" : "neutral"}>
                  {translate(item.enabled ? "shown" : "hidden")}
                </Badge>
                {!item.configured && <Badge variant="warning">{translate("notLoaded")}</Badge>}
                {(item.nameRu || item.nameEn) && (
                  <span className="text-sm">{item.nameRu || item.nameEn}</span>
                )}
                {can("settings.write") && (
                  <Button
                    className="ml-auto"
                    onClick={() => setEditing(editing === item.key ? "" : item.key)}
                    size="sm"
                    variant="ghost"
                  >
                    {translate(editing === item.key ? "close" : "configure")}
                  </Button>
                )}
              </div>
              {editing === item.key && (
                <MethodEditor
                  item={item}
                  onSaved={() => {
                    setEditing("");
                    void mutate();
                  }}
                />
              )}
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}

function numberList(value: string): number[] {
  return value
    .split(/[\s,;]+/)
    .map((part) => Number(part))
    .filter((part) => Number.isInteger(part) && part > 0);
}

function MethodEditor({ item, onSaved }: { item: PaymentMethodSettings; onSaved: () => void }) {
  const translate = useTranslations("admin.settings.paymentMethods");
  const id = useId();
  const [enabled, setEnabled] = useState(item.enabled);
  const [openDirectly, setOpenDirectly] = useState(item.openDirectly);
  const [displayOrder, setDisplayOrder] = useState(String(item.displayOrder));
  const [nameRu, setNameRu] = useState(item.nameRu);
  const [nameEn, setNameEn] = useState(item.nameEn);
  const [descriptionRu, setDescriptionRu] = useState(item.descriptionRu);
  const [descriptionEn, setDescriptionEn] = useState(item.descriptionEn);
  const [minMinor, setMinMinor] = useState(item.minMinor ? String(item.minMinor) : "");
  const [maxMinor, setMaxMinor] = useState(item.maxMinor ? String(item.maxMinor) : "");
  const [quick, setQuick] = useState(item.quickAmountsMinor.join(", "));
  const [loginKinds, setLoginKinds] = useState<string[]>(item.loginKinds);
  const [audience, setAudience] = useState(item.topUpAudience);
  const [tiers, setTiers] = useState(item.loyaltyTierCodes.join(", "));
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const text = (key: string, label: string, value: string, onChange: (value: string) => void) => (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={`${id}-${key}`}>{label}</Label>
      <Input id={`${id}-${key}`} onChange={(event) => onChange(event.target.value)} value={value} />
    </div>
  );

  return (
    <div className="mt-3 flex flex-col gap-3 border-border border-t pt-3">
      <div className="flex flex-wrap gap-4">
        <div className="flex items-center gap-2">
          <Switch checked={enabled} id={`${id}-enabled`} onCheckedChange={setEnabled} />
          <Label htmlFor={`${id}-enabled`}>{translate("enabled")}</Label>
        </div>
        <div className="flex items-center gap-2">
          <Switch checked={openDirectly} id={`${id}-direct`} onCheckedChange={setOpenDirectly} />
          <Label htmlFor={`${id}-direct`}>{translate("openDirectly")}</Label>
        </div>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {text("nameRu", translate("nameRu"), nameRu, setNameRu)}
        {text("nameEn", translate("nameEn"), nameEn, setNameEn)}
        {text("descriptionRu", translate("descriptionRu"), descriptionRu, setDescriptionRu)}
        {text("descriptionEn", translate("descriptionEn"), descriptionEn, setDescriptionEn)}
        {text("order", translate("displayOrder"), displayOrder, setDisplayOrder)}
        {text("quick", translate("quickAmounts"), quick, setQuick)}
        {text("min", translate("minMinor"), minMinor, setMinMinor)}
        {text("max", translate("maxMinor"), maxMinor, setMaxMinor)}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-audience`}>{translate("audience")}</Label>
          <Select
            onValueChange={(value) => setAudience(value as PaymentMethodSettings["topUpAudience"])}
            value={audience}
          >
            <SelectTrigger id={`${id}-audience`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["all", "first_topup", "repeat_topup"] as const).map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`audiences.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {text("tiers", translate("loyaltyTiers"), tiers, setTiers)}
      </div>
      <div className="flex flex-col gap-1.5">
        <p className="text-sm">{translate("loginKinds")}</p>
        <div className="flex flex-wrap gap-4">
          {LOGIN_KINDS.map((kind) => (
            <div className="flex items-center gap-2" key={kind}>
              <Switch
                checked={loginKinds.includes(kind)}
                id={`${id}-login-${kind}`}
                onCheckedChange={(checked) =>
                  setLoginKinds(
                    checked ? [...loginKinds, kind] : loginKinds.filter((value) => value !== kind),
                  )
                }
              />
              <Label htmlFor={`${id}-login-${kind}`}>{translate(`login.${kind}`)}</Label>
            </div>
          ))}
        </div>
        <p className="text-muted-foreground text-xs">{translate("loginKindsHint")}</p>
      </div>
      {text("reason", translate("reason"), reason, setReason)}
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
      <div>
        <Button
          disabled={pending || reason.trim() === ""}
          onClick={async () => {
            const ok = await run(`/v1/panel/settings/payment-methods/${item.provider}`, {
              body: {
                descriptionEn,
                descriptionRu,
                displayOrder: Number(displayOrder) || 0,
                enabled,
                loginKinds,
                loyaltyTierCodes: tiers
                  .split(/[\s,;]+/)
                  .map((code) => code.trim())
                  .filter(Boolean),
                maxMinor: maxMinor ? Number(maxMinor) : undefined,
                method: item.method,
                minMinor: minMinor ? Number(minMinor) : undefined,
                nameEn,
                nameRu,
                openDirectly,
                quickAmountsMinor: numberList(quick),
                topUpAudience: audience,
              },
              method: "PUT",
              reason: reason.trim(),
            });
            if (ok) {
              onSaved();
            }
          }}
          size="sm"
        >
          {translate("save")}
        </Button>
      </div>
    </div>
  );
}

/**
 * What customers are told about paying outside a provider's checkout: where
 * to send a manual transfer, and whether the wallet offers to top up through
 * support.
 */
export function PaymentGuidanceCard({
  editable,
  onSaved,
  settings,
}: {
  editable: boolean;
  onSaved: () => void;
  settings: CommerceSettings;
}) {
  const translate = useTranslations("admin.settings.paymentGuidance");
  const id = useId();
  const stored = settings.paymentGuidance;
  const [ru, setRu] = useState(stored?.manualInstructionsRu ?? "");
  const [en, setEn] = useState(stored?.manualInstructionsEn ?? "");
  const [supportTopUp, setSupportTopUp] = useState(stored?.supportTopUp ?? false);
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-ru`}>{translate("instructionsRu")}</Label>
          <Textarea
            className="min-h-24"
            disabled={!editable}
            id={`${id}-ru`}
            maxLength={2000}
            onChange={(event) => setRu(event.target.value)}
            value={ru}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-en`}>{translate("instructionsEn")}</Label>
          <Textarea
            className="min-h-24"
            disabled={!editable}
            id={`${id}-en`}
            maxLength={2000}
            onChange={(event) => setEn(event.target.value)}
            value={en}
          />
        </div>
        <div className="flex items-center gap-2">
          <Switch
            checked={supportTopUp}
            disabled={!editable}
            id={`${id}-support`}
            onCheckedChange={setSupportTopUp}
          />
          <Label htmlFor={`${id}-support`}>{translate("supportTopUp")}</Label>
        </div>
        {editable && (
          <>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-reason`}>{translate("reason")}</Label>
              <Input
                id={`${id}-reason`}
                onChange={(event) => setReason(event.target.value)}
                value={reason}
              />
            </div>
            {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
            <div>
              <Button
                disabled={pending || reason.trim() === ""}
                onClick={async () => {
                  const ok = await run("/v1/panel/settings/commerce/payment-guidance", {
                    body: {
                      manualInstructionsEn: en,
                      manualInstructionsRu: ru,
                      supportTopUp,
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
                {translate("save")}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
