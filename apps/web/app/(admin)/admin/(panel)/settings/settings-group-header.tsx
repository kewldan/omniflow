"use client";

import { useTranslations } from "next-intl";

import { PageHeader } from "@/components/admin/resource-table";

import type { SettingsGroup } from "./sections";

/**
 * The heading for one settings area.
 *
 * It reads its copy from the same keys the index cards do, so the title an
 * operator clicked is the title they land on. Two sets of words for one area is
 * how a link comes to look like it went somewhere else.
 */
export function SettingsGroupHeader({ group }: { group: SettingsGroup["key"] }) {
  const translate = useTranslations("admin.settingsIndex");
  return (
    <PageHeader
      description={translate(`groups.${group}.description`)}
      title={translate(`groups.${group}.title`)}
    />
  );
}
