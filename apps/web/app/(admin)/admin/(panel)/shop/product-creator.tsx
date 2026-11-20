"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";

type Provider = { slug: string; enabled: boolean };
type Created = { id: string };

/**
 * Create a digital-goods product: Telegram Premium for 3, 6, or 12 months, or a
 * fixed pack of Stars, delivered by a configured provider. It is created
 * hidden, with its names in both languages; the price is set in the product's
 * pricing editor before it is shown.
 */
export function ProductCreator({ onCreated }: { onCreated: () => void }) {
  const translate = useTranslations("admin.shop.productCreator");
  const ids = {
    code: useId(),
    provider: useId(),
    kind: useId(),
    amount: useId(),
    ru: useId(),
    en: useId(),
  };
  const { data: providers } = useSWR<Listing<Provider>, ApiError>(
    "/v1/panel/goods/providers",
    fetcher,
  );
  const [form, setForm] = useState({
    code: "",
    provider: "",
    kind: "telegram_premium",
    months: "3",
    stars: "",
    nameRu: "",
    nameEn: "",
  });
  const set = (patch: Partial<typeof form>) => setForm((current) => ({ ...current, ...patch }));
  const { run, pending, error } = useOperatorAction();
  const [failed, setFailed] = useState(false);

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFailed(false);
    const body =
      form.kind === "telegram_premium"
        ? { durationMonths: Number(form.months) }
        : { starQuantity: Number(form.stars) || 0 };
    let created: Created;
    try {
      created = await apiFetch<Created>("/v1/panel/goods/products", {
        body: JSON.stringify({
          code: form.code.trim().toLowerCase(),
          kind: form.kind,
          providerSlug: form.provider,
          sortOrder: 0,
          visible: false,
          ...body,
        }),
        method: "POST",
      });
    } catch {
      setFailed(true);
      return;
    }
    for (const [locale, name] of [
      ["ru", form.nameRu],
      ["en", form.nameEn],
    ] as const) {
      if (name.trim()) {
        await run(`/v1/panel/goods/products/${created.id}/localizations/${locale}`, {
          body: { description: "", name: name.trim() },
          method: "PUT",
        });
      }
    }
    toast.success(translate("created"));
    setForm({ ...form, code: "", stars: "", nameRu: "", nameEn: "" });
    onCreated();
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-3 sm:grid-cols-3" onSubmit={create}>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.code}>{translate("code")}</Label>
            <Input
              id={ids.code}
              onChange={(event) => set({ code: event.target.value })}
              placeholder="premium_3m"
              value={form.code}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.provider}>{translate("provider")}</Label>
            <Select onValueChange={(provider) => set({ provider })} value={form.provider}>
              <SelectTrigger id={ids.provider}>
                <SelectValue placeholder={translate("pickProvider")} />
              </SelectTrigger>
              <SelectContent>
                {(providers?.items ?? []).map((provider) => (
                  <SelectItem key={provider.slug} value={provider.slug}>
                    {provider.slug}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.kind}>{translate("kind")}</Label>
            <Select onValueChange={(kind) => set({ kind })} value={form.kind}>
              <SelectTrigger id={ids.kind}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="telegram_premium">
                  {translate("kinds.telegram_premium")}
                </SelectItem>
                <SelectItem value="telegram_stars">{translate("kinds.telegram_stars")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.amount}>
              {form.kind === "telegram_premium" ? translate("months") : translate("stars")}
            </Label>
            {form.kind === "telegram_premium" ? (
              <Select onValueChange={(months) => set({ months })} value={form.months}>
                <SelectTrigger id={ids.amount}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {["3", "6", "12"].map((months) => (
                    <SelectItem key={months} value={months}>
                      {translate("monthCount", { months: Number(months) })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                id={ids.amount}
                inputMode="numeric"
                onChange={(event) => set({ stars: event.target.value })}
                value={form.stars}
              />
            )}
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.ru}>{translate("nameRu")}</Label>
            <Input
              id={ids.ru}
              onChange={(event) => set({ nameRu: event.target.value })}
              value={form.nameRu}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.en}>{translate("nameEn")}</Label>
            <Input
              id={ids.en}
              onChange={(event) => set({ nameEn: event.target.value })}
              value={form.nameEn}
            />
          </div>
          {(failed || error) && (
            <p className="text-destructive text-sm sm:col-span-3" role="alert">
              {translate("invalid")}
            </p>
          )}
          <Button
            className="self-start"
            disabled={
              pending ||
              !form.code.trim() ||
              !form.provider ||
              !form.nameRu.trim() ||
              !form.nameEn.trim()
            }
            type="submit"
          >
            {translate("create")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
