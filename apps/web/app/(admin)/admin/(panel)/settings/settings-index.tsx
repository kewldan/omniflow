"use client";

import { Card, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ArrowRight } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";

import { PageHeader } from "@/components/admin/resource-table";

import { SETTINGS_GROUPS } from "./sections";

/**
 * The way in to the settings, one card per area.
 *
 * It replaces a single page that rendered everything: commerce and its payment
 * providers, ten installation sections, the telemetry preview, the backup
 * history, the diagnostics bundle, and every customer sign-in provider, in one
 * column. Finding the maintenance notice meant scrolling past the payment
 * providers, and every visit fetched all of it.
 *
 * Each card says what its area holds rather than only naming it, because a list
 * of six nouns moves the guessing from scrolling to clicking rather than
 * removing it.
 */
export function SettingsIndex() {
  const translate = useTranslations("admin.settingsIndex");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <div className="grid gap-3 sm:grid-cols-2">
        {SETTINGS_GROUPS.map((group) => (
          <Link className="group" href={group.href} key={group.key}>
            <Card className="h-full transition-colors hover:border-accent">
              <CardHeader>
                <CardTitle className="flex items-center justify-between gap-2">
                  {translate(`groups.${group.key}.title`)}
                  <ArrowRight className="size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
                </CardTitle>
                <CardDescription>{translate(`groups.${group.key}.description`)}</CardDescription>
              </CardHeader>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  );
}
