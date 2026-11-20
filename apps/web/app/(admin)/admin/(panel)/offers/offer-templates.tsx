"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { formatMoney, type Listing, type Promotion, useOperatorAction } from "@/lib/operations";

type OfferTemplate = {
  id: string;
  code: string;
  promotionId: string;
  titleRu: string;
  titleEn: string;
  termsRu: string;
  termsEn: string;
  validHours: number;
  activationHours?: number;
  bonusMinor: number;
  bonusCurrency?: string;
};
type Segment = { id: string; code: string; nameEn: string; nameRu: string; size: number };

/**
 * Offer templates — Bedolaga's promo offers — and sending one to a whole
 * segment. A template names a promotion, its copy, how long a sent offer can
 * be taken, an optional discount timer that starts when the customer takes
 * it, and an optional balance bonus paid on taking it.
 */
export function OfferTemplatesCard() {
  const translate = useTranslations("admin.offers.templates");
  const locale = useLocale();
  const ids = {
    code: useId(),
    promotion: useId(),
    ru: useId(),
    en: useId(),
    valid: useId(),
    timer: useId(),
    bonus: useId(),
  };
  const { data, mutate } = useSWR<Listing<OfferTemplate>, ApiError>(
    "/v1/panel/offers/templates",
    fetcher,
  );
  const { data: promotions } = useSWR<Listing<Promotion>, ApiError>(
    "/v1/panel/catalog/promotions",
    fetcher,
  );
  const { data: segments } = useSWR<Listing<Segment>, ApiError>(
    "/v1/panel/marketing/segments",
    fetcher,
  );
  const [form, setForm] = useState({
    code: "",
    promotionId: "",
    titleRu: "",
    titleEn: "",
    valid: "168",
    timer: "",
    bonus: "",
  });
  const [targets, setTargets] = useState<Record<string, string>>({});
  const set = (patch: Partial<typeof form>) => setForm((current) => ({ ...current, ...patch }));
  const { run, pending, error } = useOperatorAction();

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const bonus = Math.round((Number(form.bonus.replace(",", ".")) || 0) * 100);
    const ok = await run("/v1/panel/offers/templates", {
      method: "PUT",
      reason: "offer template",
      body: {
        code: form.code.trim().toLowerCase(),
        promotionId: form.promotionId,
        titleRu: form.titleRu.trim(),
        titleEn: form.titleEn.trim(),
        termsRu: "",
        termsEn: "",
        validHours: Number(form.valid) || 168,
        activationHours: form.timer ? Number(form.timer) : undefined,
        bonusMinor: bonus,
        bonusCurrency: bonus > 0 ? "RUB" : "",
      },
    });
    if (ok) {
      toast.success(translate("saved"));
      setForm({
        code: "",
        promotionId: "",
        titleRu: "",
        titleEn: "",
        valid: "168",
        timer: "",
        bonus: "",
      });
      void mutate();
    }
  }

  async function send(template: OfferTemplate) {
    const segmentId = targets[template.id];
    if (!segmentId) {
      return;
    }
    try {
      const result = await apiFetch<{ offered: number }>(
        `/v1/panel/offers/templates/${template.id}/send`,
        {
          body: JSON.stringify({ segmentId }),
          headers: { "X-Operator-Reason": "offer sent to a segment" },
          method: "POST",
        },
      );
      toast.success(translate("sent", { count: result.offered }));
    } catch {
      toast.error(translate("sendFailed"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {(data?.items ?? []).length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.template")}</TableHead>
                  <TableHead>{translate("columns.window")}</TableHead>
                  <TableHead>{translate("columns.bonus")}</TableHead>
                  <TableHead>{translate("columns.send")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(data?.items ?? []).map((template) => (
                  <TableRow key={template.id}>
                    <TableCell>
                      <div className="font-medium">
                        {locale === "ru" ? template.titleRu : template.titleEn}
                      </div>
                      <div className="font-mono text-muted-foreground text-xs">{template.code}</div>
                    </TableCell>
                    <TableCell className="text-xs">
                      {translate("window", { hours: template.validHours })}
                      {template.activationHours ? (
                        <div>{translate("timer", { hours: template.activationHours })}</div>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-xs">
                      {template.bonusMinor > 0
                        ? formatMoney(template.bonusMinor, template.bonusCurrency ?? "RUB", locale)
                        : "—"}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <Select
                          onValueChange={(segmentId) =>
                            setTargets((all) => ({ ...all, [template.id]: segmentId }))
                          }
                          value={targets[template.id] ?? ""}
                        >
                          <SelectTrigger aria-label={translate("segment")} className="w-48">
                            <SelectValue placeholder={translate("segment")} />
                          </SelectTrigger>
                          <SelectContent>
                            {(segments?.items ?? []).map((segment) => (
                              <SelectItem key={segment.id} value={segment.id}>
                                {locale === "ru" ? segment.nameRu : segment.nameEn} ({segment.size})
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                        <Button
                          disabled={!targets[template.id]}
                          onClick={() => void send(template)}
                          size="sm"
                          variant="outline"
                        >
                          {translate("sendButton")}
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        <form className="grid gap-3 sm:grid-cols-4" onSubmit={save}>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.code}>{translate("code")}</Label>
            <Input
              id={ids.code}
              onChange={(event) => set({ code: event.target.value })}
              value={form.code}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.promotion}>{translate("promotion")}</Label>
            <Select onValueChange={(promotionId) => set({ promotionId })} value={form.promotionId}>
              <SelectTrigger id={ids.promotion}>
                <SelectValue placeholder={translate("pickPromotion")} />
              </SelectTrigger>
              <SelectContent>
                {(promotions?.items ?? [])
                  .filter((promotion) => promotion.kind === "percent" || promotion.kind === "fixed")
                  .map((promotion) => (
                    <SelectItem key={promotion.id} value={promotion.id}>
                      {promotion.code}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.ru}>{translate("titleRu")}</Label>
            <Input
              id={ids.ru}
              onChange={(event) => set({ titleRu: event.target.value })}
              value={form.titleRu}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.en}>{translate("titleEn")}</Label>
            <Input
              id={ids.en}
              onChange={(event) => set({ titleEn: event.target.value })}
              value={form.titleEn}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.valid}>{translate("validHours")}</Label>
            <Input
              id={ids.valid}
              inputMode="numeric"
              onChange={(event) => set({ valid: event.target.value })}
              value={form.valid}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.timer}>{translate("activationHours")}</Label>
            <Input
              id={ids.timer}
              inputMode="numeric"
              onChange={(event) => set({ timer: event.target.value })}
              placeholder={translate("noTimer")}
              value={form.timer}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.bonus}>{translate("bonus")}</Label>
            <Input
              id={ids.bonus}
              inputMode="decimal"
              onChange={(event) => set({ bonus: event.target.value })}
              value={form.bonus}
            />
          </div>
          <Button
            className="self-end"
            disabled={
              pending ||
              !form.code.trim() ||
              !form.promotionId ||
              !form.titleRu.trim() ||
              !form.titleEn.trim()
            }
            type="submit"
          >
            {translate("save")}
          </Button>
          {error && (
            <p className="text-destructive text-sm sm:col-span-4" role="alert">
              {translate("invalid")}
            </p>
          )}
        </form>
      </CardContent>
    </Card>
  );
}
