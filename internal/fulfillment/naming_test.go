package fulfillment

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/remnawave"
)

type creatingProvisioner struct {
	recordingProvisioner
	lookedUp  []string
	createdAs []remnawave.ProvisionUser
}

func (provisioner *creatingProvisioner) UserByUsername(_ context.Context, name string) (remnawave.User, error) {
	provisioner.lookedUp = append(provisioner.lookedUp, name)
	return remnawave.User{}, remnawave.ErrNotFound
}

func (provisioner *creatingProvisioner) CreateUser(_ context.Context, desired remnawave.ProvisionUser) (remnawave.User, error) {
	provisioner.createdAs = append(provisioner.createdAs, desired)
	return remnawave.User{ID: 99, Username: desired.Username}, nil
}

func TestTemplatesNameAndDescribeANewUser(t *testing.T) {
	provisioner := &creatingProvisioner{}
	worker := NewWorker(nil, provisioner)
	worker.SetNaming(Naming{
		UsernameTemplate: "tg_{telegram_id}", DescriptionTemplate: "Omniflow {telegram_id}  {label}",
		SendTelegramID: true,
	})
	claimed := adoptedClaim("")
	claimed.remoteID = 0
	claimed.operation.Operation = "create"
	claimed.subscription.Slot = 2
	claimed.facts = namingFacts{TelegramID: 4242, ShortID: "0102030405060708", Slot: 2, Label: "Phone"}
	if _, err := worker.apply(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.createdAs) != 1 {
		t.Fatalf("created %d users", len(provisioner.createdAs))
	}
	created := provisioner.createdAs[0]
	if created.Username != "tg_4242_2" || created.Description != "Omniflow 4242 Phone" ||
		created.TelegramID == nil || *created.TelegramID != 4242 {
		t.Fatalf("created %+v", created)
	}
	// The default name is still looked up, so a user created before the
	// template existed is adopted rather than duplicated.
	if len(provisioner.lookedUp) != 2 || provisioner.lookedUp[1] != remnawaveUsername(claimed.subscription.ID) {
		t.Fatalf("looked up %v", provisioner.lookedUp)
	}
}

func TestTemplateFallsBackWhenItCannotBeUnique(t *testing.T) {
	naming := Naming{UsernameTemplate: "tg_{telegram_id}"}
	if got := naming.username(namingFacts{ShortID: "abc"}, "omniflow_x"); got != "omniflow_x" {
		t.Fatalf("a customer without Telegram got %q", got)
	}
	if got := (Naming{UsernameTemplate: "Пользователь {short_id}"}).username(
		namingFacts{ShortID: "0123456789abcdef"}, "fallback"); got != "0123456789abcdef" {
		t.Fatalf("unsafe characters must be stripped, got %q", got)
	}
	if err := (Naming{UsernameTemplate: "vpn_user"}).Validate(); err == nil {
		t.Fatal("a template with no unique placeholder was accepted")
	}
	naming, err := NamingFromEnv(func(key string) string {
		return map[string]string{"APP_REMNAWAVE_SEND_TELEGRAM_ID": "false"}[key]
	})
	if err != nil || naming.SendTelegramID {
		t.Fatalf("naming = %+v, %v", naming, err)
	}
}
