import { getTranslations } from "next-intl/server";
import { SettingsForm } from "./settings-form";

const modules = [
  "dashboard",
  "payments",
  "customers",
  "support",
  "plans",
  "marketing",
  "referrals",
  "system",
  "security",
] as const;

export default async function AdminHome() {
  const translate = await getTranslations("admin");
  return (
    <main className="mx-auto grid min-h-screen max-w-7xl gap-8 p-8 lg:grid-cols-[240px_1fr]">
      <aside className="rounded-2xl border border-slate-800 bg-slate-950 p-5">
        <p className="mb-6 text-xl font-semibold">{translate("product")}</p>
        <nav className="grid gap-1">
          {modules.map((module) => (
            <span className="rounded-lg px-3 py-2 text-sm text-slate-400" key={module}>
              {translate(`navigation.${module}`)}
            </span>
          ))}
        </nav>
      </aside>
      <section>
        <p className="text-sm font-medium text-sky-400">{translate("eyebrow")}</p>
        <h1 className="mt-2 text-4xl font-semibold tracking-tight">{translate("title")}</h1>
        <p className="mt-3 max-w-2xl text-slate-400">{translate("description")}</p>
        <div className="mt-8 max-w-xl rounded-2xl border border-slate-800 bg-slate-950 p-6">
          <SettingsForm />
        </div>
      </section>
    </main>
  );
}
