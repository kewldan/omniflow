"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";

type Pinned = {
  id: string;
  textRu: string;
  textEn: string;
  photoUrl?: string;
  pin: boolean;
  showOnStart: boolean;
  status: "active" | "retired";
  sent: number;
  failed: number;
  createdAt: string;
};

/**
 * The pinned message: one message the bot sends to every customer, pins in
 * the chat when asked, and shows on /start to whoever has not had it.
 * Publishing a new one retires the old one, which the bot unpins everywhere.
 */
export function PinnedMessageCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.pinned");
  const locale = useLocale();
  const ids = { ru: useId(), en: useId(), photo: useId(), pin: useId(), start: useId() };
  const { data, mutate } = useSWR<Listing<Pinned>, ApiError>("/v1/panel/marketing/pinned", fetcher);
  const [form, setForm] = useState({
    textRu: "",
    textEn: "",
    photoUrl: "",
    pin: true,
    showOnStart: true,
  });
  const set = (patch: Partial<typeof form>) => setForm((current) => ({ ...current, ...patch }));
  const { run, pending, error } = useOperatorAction();
  const active = (data?.items ?? []).find((item) => item.status === "active");

  async function publish(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      await run("/v1/panel/marketing/pinned", {
        method: "POST",
        body: form,
        reason: "pinned message",
      })
    ) {
      toast.success(translate("published"));
      setForm({ textRu: "", textEn: "", photoUrl: "", pin: true, showOnStart: true });
      void mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {active ? (
          <div className="flex flex-col gap-2 rounded-md border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="success">{translate("active")}</Badge>
              <Timestamp value={active.createdAt} />
              <span className="text-muted-foreground text-xs">
                {translate("reach", { sent: active.sent, failed: active.failed })}
              </span>
            </div>
            <p className="whitespace-pre-wrap">{locale === "ru" ? active.textRu : active.textEn}</p>
            {editable && (
              <Button
                className="self-start"
                disabled={pending}
                onClick={async () => {
                  if (
                    await run(`/v1/panel/marketing/pinned/${active.id}/retire`, {
                      method: "POST",
                      reason: "pinned message retired",
                    })
                  ) {
                    void mutate();
                  }
                }}
                size="sm"
                variant="outline"
              >
                {translate("retire")}
              </Button>
            )}
          </div>
        ) : (
          <p className="text-muted-foreground text-sm">{translate("none")}</p>
        )}
        {editable && (
          <form className="flex flex-col gap-3" onSubmit={publish}>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.ru}>{translate("textRu")}</Label>
                <Textarea
                  id={ids.ru}
                  maxLength={3500}
                  onChange={(event) => set({ textRu: event.target.value })}
                  rows={4}
                  value={form.textRu}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.en}>{translate("textEn")}</Label>
                <Textarea
                  id={ids.en}
                  maxLength={3500}
                  onChange={(event) => set({ textEn: event.target.value })}
                  rows={4}
                  value={form.textEn}
                />
              </div>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.photo}>{translate("photo")}</Label>
              <Input
                id={ids.photo}
                inputMode="url"
                onChange={(event) => set({ photoUrl: event.target.value })}
                placeholder="https://…"
                value={form.photoUrl}
              />
            </div>
            <div className="flex flex-wrap gap-6">
              <div className="flex items-center gap-2">
                <Switch checked={form.pin} id={ids.pin} onCheckedChange={(pin) => set({ pin })} />
                <Label htmlFor={ids.pin}>{translate("pin")}</Label>
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  checked={form.showOnStart}
                  id={ids.start}
                  onCheckedChange={(showOnStart) => set({ showOnStart })}
                />
                <Label htmlFor={ids.start}>{translate("showOnStart")}</Label>
              </div>
            </div>
            {error && (
              <p className="text-destructive text-sm" role="alert">
                {translate("invalid")}
              </p>
            )}
            <Button
              className="self-start"
              disabled={pending || !form.textRu.trim() || !form.textEn.trim()}
              type="submit"
            >
              {translate("publish")}
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}
