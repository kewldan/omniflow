package importservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// Exclusions are the panel users an import never turns into customers: the
// operator lists them by username or by squad in the `remnawave` settings
// section (importExcludedUsernames, importExcludedSquads). A service account
// a proxy chain stands on is the case it exists for — made a customer, it
// would be reconciled, expired, and disabled like one.
type Exclusions struct {
	usernames map[string]bool
	squads    map[string]bool
}

// NewExclusions builds the list. Usernames match exactly, ignoring case; a
// squad matches by its UUID or its name, ignoring case.
func NewExclusions(usernames, squads []string) Exclusions {
	exclusions := Exclusions{usernames: map[string]bool{}, squads: map[string]bool{}}
	for _, name := range usernames {
		if name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@"))); name != "" {
			exclusions.usernames[name] = true
		}
	}
	for _, squad := range squads {
		if squad = strings.ToLower(strings.TrimSpace(squad)); squad != "" {
			exclusions.squads[squad] = true
		}
	}
	return exclusions
}

// Username reports whether a panel username is excluded.
func (exclusions Exclusions) Username(username string) bool {
	return exclusions.usernames[strings.ToLower(strings.TrimSpace(username))]
}

// User reports whether a panel user is excluded by username or by squad.
func (exclusions Exclusions) User(user remnawave.User) bool {
	if exclusions.Username(user.Username) {
		return true
	}
	for _, squad := range user.ActiveInternalSquads {
		if exclusions.squads[strings.ToLower(squad.UUID)] || exclusions.squads[strings.ToLower(strings.TrimSpace(squad.Name))] {
			return true
		}
	}
	return false
}

// exclusions reads the operator's list from the `remnawave` settings section.
// An installation that never saved the section excludes nobody.
func (service *Service) exclusions(ctx context.Context, queries *dbgen.Queries) (Exclusions, error) {
	row, err := queries.GetSettingSection(ctx, "remnawave")
	if errors.Is(err, pgx.ErrNoRows) {
		return NewExclusions(nil, nil), nil
	}
	if err != nil {
		return Exclusions{}, err
	}
	var document installsettings.Document
	if len(row.Document) > 0 {
		if err := json.Unmarshal(row.Document, &document); err != nil {
			return Exclusions{}, err
		}
	}
	return NewExclusions(splitLines(document.Strings("importExcludedUsernames", nil)),
		splitLines(document.Strings("importExcludedSquads", nil))), nil
}

// splitLines accepts the list one per line as well as comma-separated, which
// is how an operator types it into a text area.
func splitLines(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}
