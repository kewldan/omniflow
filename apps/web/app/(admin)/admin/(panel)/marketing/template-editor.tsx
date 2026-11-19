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

type TemplateButton = { key: string; textRu: string; textEn: string; url?: string; route?: string };

/** Bot sections a button may open; mirrors the API's closed list. */
const ROUTES = [
  "plans",
  "subs",
  "wallet",
  "topup",
  "referral",
  "support",
  "shop",
  "gifts",
  "offers",
  "connect",
  "news",
  "home",
];

/** Placeholders the bot fills per recipient; mirrors commerce.TemplateVariables. */
export const TEMPLATE_VARIABLES = [
  "name",
  "days_left",
  "plan",
  "expires_at",
  "balance",
  "referral_code",
  "referral_link",
  "loyalty_tier",
  "loyalty_discount",
] as const;

/** The supported placeholders a template's texts use, which is what it declares. */
export function usedVariables(...texts: string[]): string[] {
  const found = new Set<string>();
  for (const text of texts) {
    for (const match of text.matchAll(/\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}/g)) {
      found.add(match[1] ?? "");
    }
  }
  return [...found];
}

/**
 * Write a broadcast template: subject and body in both languages, an optional
 * picture, and up to six inline buttons. Saving under an existing code
 * replaces that template. The API refuses a body with markup Telegram would
 * reject, so a broken template never reaches a campaign.
 */
export function TemplateEditorCard({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.marketing.templateEditor");
  const ids = {
    code: useId(),
    cls: useId(),
    subjectRu: useId(),
    subjectEn: useId(),
    bodyRu: useId(),
    bodyEn: useId(),
    photo: useId(),
  };
  const empty = {
    code: "",
    class: "marketing",
    subjectRu: "",
    subjectEn: "",
    bodyRu: "",
    bodyEn: "",
    photoUrl: "",
  };
  const [form, setForm] = useState(empty);
  const [buttons, setButtons] = useState<TemplateButton[]>([]);
  const { run, pending, error } = useOperatorAction();

  function set(patch: Partial<typeof form>) {
    setForm((current) => ({ ...current, ...patch }));
  }
  function setButton(index: number, patch: Partial<TemplateButton>) {
    setButtons((current) =>
      current.map((button, at) => (at === index ? { ...button, ...patch } : button)),
    );
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const ok = await run("/v1/panel/marketing/templates", {
      body: {
        code: form.code.trim().toLowerCase(),
        class: form.class,
        subjectRu: form.subjectRu,
        subjectEn: form.subjectEn,
        bodyRu: form.bodyRu,
        bodyEn: form.bodyEn,
        variables: usedVariables(form.subjectRu, form.subjectEn, form.bodyRu, form.bodyEn),
        photoUrl: form.photoUrl.trim(),
        buttons: buttons.map((button) =>
          button.route
            ? { textRu: button.textRu, textEn: button.textEn, route: button.route }
            : { textRu: button.textRu, textEn: button.textEn, url: button.url },
        ),
      },
      method: "PUT",
      reason: "message template",
    });
    if (ok) {
      toast.success(translate("saved"));
      setForm(empty);
      setButtons([]);
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
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.code}>{translate("code")}</Label>
              <Input
                id={ids.code}
                onChange={(event) => set({ code: event.target.value })}
                placeholder="comeback_november"
                value={form.code}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.cls}>{translate("class")}</Label>
              <Select onValueChange={(value) => set({ class: value })} value={form.class}>
                <SelectTrigger id={ids.cls}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="marketing">{translate("classes.marketing")}</SelectItem>
                  <SelectItem value="transactional">
                    {translate("classes.transactional")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.subjectRu}>{translate("subjectRu")}</Label>
              <Input
                id={ids.subjectRu}
                onChange={(event) => set({ subjectRu: event.target.value })}
                value={form.subjectRu}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.subjectEn}>{translate("subjectEn")}</Label>
              <Input
                id={ids.subjectEn}
                onChange={(event) => set({ subjectEn: event.target.value })}
                value={form.subjectEn}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.bodyRu}>{translate("bodyRu")}</Label>
              <Textarea
                id={ids.bodyRu}
                maxLength={3500}
                onChange={(event) => set({ bodyRu: event.target.value })}
                rows={6}
                value={form.bodyRu}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.bodyEn}>{translate("bodyEn")}</Label>
              <Textarea
                id={ids.bodyEn}
                maxLength={3500}
                onChange={(event) => set({ bodyEn: event.target.value })}
                rows={6}
                value={form.bodyEn}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1 rounded-md border border-dashed p-3 text-sm">
            <div className="font-medium">{translate("variablesTitle")}</div>
            <p className="text-muted-foreground text-xs">{translate("variablesHint")}</p>
            <ul className="mt-1 grid gap-1 sm:grid-cols-2">
              {TEMPLATE_VARIABLES.map((variable) => (
                <li className="text-xs" key={variable}>
                  <code className="font-mono">{`{{${variable}}}`}</code> —{" "}
                  {translate(`variables.${variable}`)}
                </li>
              ))}
            </ul>
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

          <div className="flex flex-col gap-2">
            <div className="font-medium text-sm">{translate("buttons")}</div>
            {buttons.map((button, index) => (
              <div className="grid gap-2 sm:grid-cols-[1fr_1fr_9rem_1fr_auto]" key={button.key}>
                <Input
                  aria-label={translate("buttonTextRu")}
                  onChange={(event) => setButton(index, { textRu: event.target.value })}
                  placeholder={translate("buttonTextRu")}
                  value={button.textRu}
                />
                <Input
                  aria-label={translate("buttonTextEn")}
                  onChange={(event) => setButton(index, { textEn: event.target.value })}
                  placeholder={translate("buttonTextEn")}
                  value={button.textEn}
                />
                <Select
                  onValueChange={(value) =>
                    setButton(
                      index,
                      value === "url"
                        ? { route: undefined, url: button.url ?? "" }
                        : { route: value, url: undefined },
                    )
                  }
                  value={button.route ?? "url"}
                >
                  <SelectTrigger aria-label={translate("buttonTarget")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="url">{translate("link")}</SelectItem>
                    {ROUTES.map((route) => (
                      <SelectItem key={route} value={route}>
                        {translate(`routes.${route}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {button.route ? (
                  <span />
                ) : (
                  <Input
                    aria-label={translate("link")}
                    onChange={(event) => setButton(index, { url: event.target.value })}
                    placeholder="https://…"
                    value={button.url ?? ""}
                  />
                )}
                <Button
                  onClick={() => setButtons((current) => current.filter((_, at) => at !== index))}
                  type="button"
                  variant="outline"
                >
                  {translate("remove")}
                </Button>
              </div>
            ))}
            {buttons.length < 6 && (
              <Button
                className="self-start"
                onClick={() =>
                  setButtons((current) => [
                    ...current,
                    { key: crypto.randomUUID(), textRu: "", textEn: "", route: "plans" },
                  ])
                }
                type="button"
                variant="outline"
              >
                {translate("addButton")}
              </Button>
            )}
          </div>

          {error && (
            <p className="text-destructive text-sm" role="alert">
              {translate("invalid")}
            </p>
          )}
          <Button
            className="self-start"
            disabled={pending || !form.code.trim() || !form.bodyRu.trim() || !form.bodyEn.trim()}
            type="submit"
          >
            {translate("save")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
