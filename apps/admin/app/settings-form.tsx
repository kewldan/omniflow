"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@omniflow/ui/button";
import { useTranslations } from "next-intl";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { create } from "zustand";

const schema = z.object({
  locale: z.enum(["ru", "en"]),
  telemetryEnabled: z.boolean(),
});

type Settings = z.infer<typeof schema>;

const useSettingsPreview = create<Settings>(() => ({ locale: "ru", telemetryEnabled: true }));

export function SettingsForm() {
  const translate = useTranslations("settings");
  const setPreview = useSettingsPreview.setState;
  const { register, handleSubmit, formState } = useForm<Settings>({
    resolver: zodResolver(schema),
    defaultValues: { locale: "ru", telemetryEnabled: true },
  });

  return (
    <form className="grid gap-5" onSubmit={handleSubmit((values) => setPreview(values))}>
      <div>
        <h2 className="text-lg font-semibold">{translate("title")}</h2>
        <p className="mt-1 text-sm text-slate-400">{translate("description")}</p>
      </div>
      <label className="grid gap-2 text-sm">
        {translate("locale")}
        <select
          className="rounded-lg border border-slate-700 bg-slate-900 px-3 py-2"
          {...register("locale")}
        >
          <option value="ru">{translate("russian")}</option>
          <option value="en">{translate("english")}</option>
        </select>
      </label>
      <label className="flex items-center gap-3 text-sm">
        <input type="checkbox" {...register("telemetryEnabled")} /> {translate("telemetry")}
      </label>
      <Button disabled={formState.isSubmitting} type="submit">
        {translate("save")}
      </Button>
    </form>
  );
}
