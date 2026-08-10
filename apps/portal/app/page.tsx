import { Button } from "@omniflow/ui/button";
import { getTranslations } from "next-intl/server";

export default async function PortalHome() {
  const translate = await getTranslations("home");
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl items-center px-6 py-20">
      <section className="max-w-2xl">
        <p className="text-sm font-medium text-sky-400">{translate("eyebrow")}</p>
        <h1 className="mt-3 text-5xl font-semibold tracking-tight">{translate("title")}</h1>
        <p className="mt-5 text-lg leading-8 text-slate-400">{translate("description")}</p>
        <Button className="mt-8">{translate("action")}</Button>
      </section>
    </main>
  );
}
