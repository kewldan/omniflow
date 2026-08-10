import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { getTranslations } from "next-intl/server";

export default async function CustomerHome() {
  const translate = await getTranslations("home");
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl items-center px-6 py-20">
      <Card className="max-w-2xl border-slate-800 bg-slate-950/80">
        <CardHeader>
          <p className="text-sm font-medium text-sky-400">{translate("eyebrow")}</p>
          <CardTitle className="text-5xl tracking-tight">{translate("title")}</CardTitle>
          <CardDescription className="text-lg leading-8">
            {translate("description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button>{translate("action")}</Button>
        </CardContent>
      </Card>
    </main>
  );
}
