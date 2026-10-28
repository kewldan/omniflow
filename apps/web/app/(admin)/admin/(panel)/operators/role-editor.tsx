"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription } from "@omniflow/ui/alert";
import { Button } from "@omniflow/ui/button";
import { Checkbox } from "@omniflow/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { AlertTriangle } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError, apiFetch } from "@/lib/api";
import { useSession } from "@/lib/session";

import {
  assignable,
  type Operator,
  orderedRoles,
  type RbacCatalog,
  ROLES,
  type Role,
} from "./roles";

const schema = z.object({ roles: z.array(z.enum(ROLES)).min(1) });
type Values = z.infer<typeof schema>;

/**
 * Changing what an existing operator may do.
 *
 * A role the signed-in operator could not grant is shown but disabled, in both
 * directions: they cannot add it, and if the target holds it they cannot take
 * it away. The API applies the same rule and is what actually enforces it; the
 * disabled box only saves somebody a refused request. The editor is never
 * offered for the operator's own row — the API refuses that too.
 */
export function RoleEditor({
  catalog,
  onChanged,
  onOpenChange,
  operator,
}: {
  catalog: RbacCatalog | undefined;
  onChanged: () => void;
  onOpenChange: (open: boolean) => void;
  operator: Operator | null;
}) {
  const translate = useTranslations("admin.operators.roleEditor");
  const translateRole = useTranslations("admin.roles");
  const { can, session } = useSession();
  const [formError, setFormError] = useState<string | null>(null);
  const rolesId = useId();
  const actorRoles = session?.account.roles ?? [];

  const form = useForm<Values>({
    defaultValues: { roles: operator?.roles ?? [] },
    resolver: zodResolver(schema),
  });

  useEffect(() => {
    form.reset({ roles: operator?.roles ?? [] });
    setFormError(null);
  }, [operator, form]);

  async function submit(values: Values) {
    if (!operator) {
      return;
    }
    setFormError(null);
    try {
      await apiFetch(`/v1/panel/admins/${operator.id}/roles`, {
        body: JSON.stringify({ roles: orderedRoles(values.roles) }),
        method: "PUT",
      });
      toast.success(translate("saved", { name: operator.displayName }));
      onChanged();
      onOpenChange(false);
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.code : null;
      const known: Record<string, string> = {
        last_owner: "errors.lastOwner",
        role_escalation: "errors.escalation",
        self_role_change: "errors.self",
      };
      setFormError(translate(code && known[code] ? known[code] : "errors.generic"));
    }
  }

  return (
    <Dialog onOpenChange={onOpenChange} open={Boolean(operator)}>
      <DialogContent>
        <form className="grid gap-4" noValidate onSubmit={form.handleSubmit(submit)}>
          <DialogHeader>
            <DialogTitle>{translate("title", { name: operator?.displayName ?? "" })}</DialogTitle>
            <DialogDescription>{translate("description")}</DialogDescription>
          </DialogHeader>

          {formError ? (
            <Alert variant="danger">
              <AlertTriangle />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          ) : null}

          <fieldset className="flex flex-col gap-2">
            <legend className="sr-only">{translate("legend")}</legend>
            <Controller
              control={form.control}
              name="roles"
              render={({ field }) => (
                <div className="grid gap-2 sm:grid-cols-2">
                  {ROLES.map((role) => {
                    const checkboxId = `${rolesId}-${role}`;
                    const allowed = assignable(role, catalog, can, actorRoles);
                    return (
                      <div className="flex items-center gap-2" key={role}>
                        <Checkbox
                          checked={field.value.includes(role)}
                          disabled={!allowed}
                          id={checkboxId}
                          onCheckedChange={(checked) =>
                            field.onChange(
                              checked
                                ? [...field.value, role]
                                : field.value.filter((value: Role) => value !== role),
                            )
                          }
                        />
                        <Label htmlFor={checkboxId}>{translateRole(role)}</Label>
                      </div>
                    );
                  })}
                </div>
              )}
            />
            {form.formState.errors.roles ? (
              <p className="text-destructive text-xs">{translate("errors.roles")}</p>
            ) : null}
          </fieldset>

          <p className="text-muted-foreground text-xs">{translate("notice")}</p>

          <DialogFooter>
            <Button onClick={() => onOpenChange(false)} type="button" variant="outline">
              {translate("cancel")}
            </Button>
            <Button disabled={form.formState.isSubmitting || !form.formState.isDirty} type="submit">
              {form.formState.isSubmitting ? translate("saving") : translate("save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
