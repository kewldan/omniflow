"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { Plus, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Section = { section: string; document: Record<string, unknown>; version: number };
type CustomButton = { labelRu: string; labelEn: string; url: string; kind: "url" | "webapp" };
type TextOverride = { ru?: string; en?: string };
type Catalog = { items: { key: string; ru: string; en: string }[] };

/**
 * The bot's own menu buttons and wording. Custom buttons open a link or a
 * Mini App and sit above the refresh row; text overrides replace any bot
 * message, key by key, in either language. Both are stored in the bot section
 * of the installation settings and reach the bot within a minute.
 */
export function BotCustomization() {
  const translate = useTranslations("admin.botCustomization");
  const { can } = useSession();
  const editable = can("settings.write");
  const { data: sections, mutate } = useSWR<Listing<Section>, ApiError>(
    "/v1/panel/settings",
    fetcher,
  );
  const { data: catalog } = useSWR<Catalog, ApiError>("/v1/panel/bot/texts", fetcher);
  const bot = sections?.items?.find((item) => item.section === "bot");
  const [buttons, setButtons] = useState<CustomButton[]>([]);
  const [texts, setTexts] = useState<Record<string, TextOverride>>({});
  const [search, setSearch] = useState("");
  const { run, pending } = useOperatorAction();
  const searchId = useId();

  useEffect(() => {
    if (!bot) {
      return;
    }
    setButtons(Array.isArray(bot.document.buttons) ? (bot.document.buttons as CustomButton[]) : []);
    setTexts(
      bot.document.texts && typeof bot.document.texts === "object"
        ? (bot.document.texts as Record<string, TextOverride>)
        : {},
    );
  }, [bot]);

  if (!bot) {
    return null;
  }

  async function save() {
    if (!bot) {
      return;
    }
    const cleaned = Object.fromEntries(
      Object.entries(texts).filter(
        ([, value]) => (value.ru ?? "").trim() || (value.en ?? "").trim(),
      ),
    );
    if (
      await run("/v1/panel/settings/bot", {
        method: "PUT",
        body: {
          document: { ...bot.document, buttons, texts: cleaned },
          version: bot.version,
          secrets: {},
        },
        reason: "bot customization",
      })
    ) {
      toast.success(translate("saved"));
      await mutate();
    } else {
      toast.error(translate("saveFailed"));
    }
  }

  const needle = search.trim().toLowerCase();
  const matches = needle
    ? (catalog?.items ?? [])
        .filter(
          (item) =>
            item.key.toLowerCase().includes(needle) ||
            item.ru.toLowerCase().includes(needle) ||
            item.en.toLowerCase().includes(needle),
        )
        .slice(0, 25)
    : (catalog?.items ?? []).filter((item) => texts[item.key]);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="font-medium text-sm">{translate("buttons")}</div>
        {buttons.map((button, index) => (
          <div
            className="grid gap-2 sm:grid-cols-[1fr_1fr_2fr_8rem_auto]"
            key={`${button.url}-${index.toString()}`}
          >
            {(["labelRu", "labelEn", "url"] as const).map((field) => (
              <Input
                aria-label={translate(field)}
                disabled={!editable}
                key={field}
                onChange={(event) =>
                  setButtons(
                    buttons.map((item, at) =>
                      at === index ? { ...item, [field]: event.target.value } : item,
                    ),
                  )
                }
                placeholder={translate(field)}
                value={button[field]}
              />
            ))}
            <Select
              disabled={!editable}
              onValueChange={(kind) =>
                setButtons(
                  buttons.map((item, at) =>
                    at === index ? { ...item, kind: kind as CustomButton["kind"] } : item,
                  ),
                )
              }
              value={button.kind}
            >
              <SelectTrigger aria-label={translate("kind")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="url">{translate("kinds.url")}</SelectItem>
                <SelectItem value="webapp">{translate("kinds.webapp")}</SelectItem>
              </SelectContent>
            </Select>
            <Button
              aria-label={translate("remove")}
              disabled={!editable}
              onClick={() => setButtons(buttons.filter((_, at) => at !== index))}
              size="icon"
              variant="ghost"
            >
              <Trash2 aria-hidden />
            </Button>
          </div>
        ))}
        {editable ? (
          <Button
            className="self-start"
            onClick={() =>
              setButtons([...buttons, { labelRu: "", labelEn: "", url: "https://", kind: "url" }])
            }
            size="sm"
            variant="secondary"
          >
            <Plus aria-hidden />
            {translate("addButton")}
          </Button>
        ) : null}

        <div className="flex flex-col gap-1">
          <Label htmlFor={searchId}>{translate("texts")}</Label>
          <Input
            id={searchId}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={translate("searchPlaceholder")}
            value={search}
          />
          <span className="text-muted-foreground text-xs">{translate("textsHint")}</span>
        </div>
        {matches.length > 0 ? (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("key")}</TableHead>
                  <TableHead>{translate("ru")}</TableHead>
                  <TableHead>{translate("en")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {matches.map((item) => (
                  <TableRow key={item.key}>
                    <TableCell className="font-mono text-xs">{item.key}</TableCell>
                    {(["ru", "en"] as const).map((locale) => (
                      <TableCell key={locale}>
                        <Textarea
                          aria-label={`${item.key} ${locale}`}
                          disabled={!editable}
                          onChange={(event) =>
                            setTexts({
                              ...texts,
                              [item.key]: { ...texts[item.key], [locale]: event.target.value },
                            })
                          }
                          placeholder={item[locale]}
                          rows={2}
                          value={texts[item.key]?.[locale] ?? ""}
                        />
                      </TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        ) : null}
        {editable ? (
          <Button className="self-start" disabled={pending} onClick={save}>
            {translate("save")}
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}
