"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
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
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { AlertTriangle, Copy } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError, apiFetch } from "@/lib/api";
import { useSession } from "@/lib/session";

import {
  assignable,
  type Invitation,
  invitationLink,
  orderedRoles,
  type RbacCatalog,
  ROLES,
  type Role,
} from "./roles";

const schema = z.object({
  email: z.email(),
  roles: z.array(z.enum(ROLES)).min(1),
});

type Values = z.infer<typeof schema>;

type Issued = { invitation: Invitation; token: string };

/**
 * Inviting an operator, in two stages inside one dialog.
 *
 * First the address and the roles. Then the link — shown exactly once, because
 * the server keeps only its digest. There is no email transport, so the owner
 * copies it and sends it however they talk to the person. Losing it is not a
 * disaster: inviting the same address again retires the old link and issues a
 * new one, which the second stage says plainly.
 */
export function InviteDialog({
  catalog,
  onInvited,
  onOpenChange,
  open,
}: {
  catalog: RbacCatalog | undefined;
  onInvited: () => void;
  onOpenChange: (open: boolean) => void;
  open: boolean;
}) {
  const translate = useTranslations("admin.operators.invite");
  const translateRole = useTranslations("admin.roles");
  const { can, session } = useSession();
  const [issued, setIssued] = useState<Issued | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const emailId = useId();
  const linkId = useId();
  const rolesId = useId();

  const form = useForm<Values>({
    defaultValues: { email: "", roles: ["support"] },
    resolver: zodResolver(schema),
  });

  function close(next: boolean) {
    if (!next) {
      setIssued(null);
      setFormError(null);
      form.reset();
    }
    onOpenChange(next);
  }

  async function submit(values: Values) {
    setFormError(null);
    try {
      const result = await apiFetch<Issued>("/v1/panel/admins/invitations", {
        body: JSON.stringify({ email: values.email.trim(), roles: orderedRoles(values.roles) }),
        method: "POST",
      });
      setIssued(result);
      onInvited();
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.code : null;
      if (code === "email_in_use") {
        form.setError("email", { type: "conflict" });
        return;
      }
      if (code === "role_escalation") {
        setFormError(translate("errors.escalation"));
        return;
      }
      setFormError(translate("errors.generic"));
    }
  }

  async function copy(link: string) {
    try {
      await navigator.clipboard.writeText(link);
      toast.success(translate("copied"));
    } catch {
      toast.error(translate("copyFailed"));
    }
  }

  const actorRoles = session?.account.roles ?? [];
  const link = issued ? invitationLink(issued.token) : "";

  return (
    <Dialog onOpenChange={close} open={open}>
      <DialogContent className="max-w-lg">
        {issued ? (
          <>
            <DialogHeader>
              <DialogTitle>{translate("issuedTitle")}</DialogTitle>
              <DialogDescription>
                {translate("issuedDescription", { email: issued.invitation.email })}
              </DialogDescription>
            </DialogHeader>
            <Alert variant="warning">
              <AlertTriangle />
              <AlertTitle>{translate("onlyOnceTitle")}</AlertTitle>
              <AlertDescription>{translate("onlyOnce")}</AlertDescription>
            </Alert>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={linkId}>{translate("link")}</Label>
              <div className="flex gap-2">
                <Input
                  className="font-mono text-[12px]"
                  id={linkId}
                  onFocus={(event) => event.currentTarget.select()}
                  readOnly
                  value={link}
                />
                <Button onClick={() => copy(link)} type="button" variant="secondary">
                  <Copy aria-hidden />
                  {translate("copy")}
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">
                {translate("expires", { date: new Date(issued.invitation.expiresAt) })}
              </p>
            </div>
            <DialogFooter>
              <Button onClick={() => close(false)} type="button">
                {translate("done")}
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form className="grid gap-4" noValidate onSubmit={form.handleSubmit(submit)}>
            <DialogHeader>
              <DialogTitle>{translate("title")}</DialogTitle>
              <DialogDescription>{translate("description")}</DialogDescription>
            </DialogHeader>

            {formError ? (
              <Alert variant="danger">
                <AlertTriangle />
                <AlertDescription>{formError}</AlertDescription>
              </Alert>
            ) : null}

            <div className="flex flex-col gap-1.5">
              <Label htmlFor={emailId}>{translate("email")}</Label>
              <Input
                aria-describedby={form.formState.errors.email ? `${emailId}-error` : undefined}
                aria-invalid={Boolean(form.formState.errors.email)}
                autoComplete="off"
                id={emailId}
                type="email"
                {...form.register("email")}
              />
              {form.formState.errors.email ? (
                <p className="text-destructive text-xs" id={`${emailId}-error`}>
                  {form.formState.errors.email.type === "conflict"
                    ? translate("errors.emailInUse")
                    : translate("errors.email")}
                </p>
              ) : null}
            </div>

            <fieldset aria-describedby={`${rolesId}-hint`} className="flex flex-col gap-2">
              <legend className="mb-1 font-medium text-sm">{translate("roles")}</legend>
              <Controller
                control={form.control}
                name="roles"
                render={({ field }) => (
                  <div className="grid gap-2 sm:grid-cols-2">
                    {ROLES.map((role) => {
                      const allowed = assignable(role, catalog, can, actorRoles);
                      const checkboxId = `${rolesId}-${role}`;
                      return (
                        <div className="flex items-start gap-2" key={role}>
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
                          <Label className="flex flex-col items-start gap-0.5" htmlFor={checkboxId}>
                            <span>{translateRole(role)}</span>
                            <span className="font-normal text-muted-foreground text-xs">
                              {translate(`roleHints.${role}`)}
                            </span>
                          </Label>
                        </div>
                      );
                    })}
                  </div>
                )}
              />
              <p className="text-muted-foreground text-xs" id={`${rolesId}-hint`}>
                {translate("rolesHint")}
              </p>
              {form.formState.errors.roles ? (
                <p className="text-destructive text-xs">{translate("errors.roles")}</p>
              ) : null}
            </fieldset>

            <DialogFooter>
              <Button onClick={() => close(false)} type="button" variant="outline">
                {translate("cancel")}
              </Button>
              <Button disabled={form.formState.isSubmitting} type="submit">
                {form.formState.isSubmitting ? translate("submitting") : translate("submit")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
