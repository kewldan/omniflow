package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adtracking"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountAnalytics registers the operator's advertising measurement.
//
// Configuration is `settings`; the reports are `finance.read`, because a
// conversion export is revenue data and the person who reads the sales report
// is the person who reads this.
func (handlers *AdminHandlers) mountAnalytics(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/analytics", handlers.analyticsSettings)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/analytics", handlers.saveAnalyticsSettings)

	// The reports are `finance.read` — the same permission the sales report and
	// payment health carry. A conversion export is revenue data with an
	// advertising label on it, and the person who reads one reads the other.
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/reports/channels", handlers.channelReport)
		read.Get("/reports/conversions", handlers.conversionExport)
	})
}

// mountPublicAnalytics publishes what an anonymous visitor's browser renders.
//
// Public, because the storefront is read before anybody has signed in and the
// answer is the operator's own configuration rather than anything about a
// person. It carries verification tags unconditionally — they observe nobody —
// and counter identifiers only when measurement is enabled.
func (handlers *AdminHandlers) mountPublicAnalytics(router chi.Router) {
	if handlers.operations == nil {
		return
	}
	router.Get("/v1/analytics", func(writer http.ResponseWriter, request *http.Request) {
		settings, err := handlers.operations.PublicAnalytics(request.Context())
		if err != nil {
			// The storefront must render without this. An installation whose
			// analytics row cannot be read has a page with no counter on it,
			// not a page that fails.
			writeJSON(writer, http.StatusOK, map[string]any{"measurable": false})
			return
		}
		writeJSON(writer, http.StatusOK, settings)
	})
}

func (handlers *AdminHandlers) analyticsSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.operations.Analytics(request.Context())
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveAnalyticsSettings(
	writer http.ResponseWriter, request *http.Request,
) {
	var body struct {
		adtracking.Settings
		Version int32 `json:"version"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.operations.SaveAnalytics(
		request.Context(), body.Settings, body.Version, actorFrom(request))
	handlers.respond(writer, request, settings, err)
}

// channelReport says what each channel brought in, which is the question an
// operator asks before exporting anything.
func (handlers *AdminHandlers) channelReport(writer http.ResponseWriter, request *http.Request) {
	since, until, _, _ := reportPeriod(request)
	channels, err := handlers.operations.Channels(request.Context(), since, until)
	handlers.respond(writer, request, map[string]any{"channels": channels}, err)
}

// conversionExport is the file an operator uploads to their advertising
// platform.
//
// This software makes no request to any advertising network. The export is a
// download, and what happens to it next is the operator's decision — sending a
// customer's purchase to Google because a settings toggle was on is not
// something anybody should have to discover from behaviour.
func (handlers *AdminHandlers) conversionExport(writer http.ResponseWriter, request *http.Request) {
	since, until, _, _ := reportPeriod(request)
	conversions, err := handlers.operations.Conversions(
		request.Context(), since, until, query(request, "source"))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	if query(request, "format") != "csv" {
		handlers.respond(writer, request, map[string]any{"conversions": conversions}, nil)
		return
	}

	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="conversions-%s-%s.csv"`,
		since.Format("2006-01-02"), until.Format("2006-01-02")))
	rows := csv.NewWriter(writer)
	// The header is the column set an advertising platform's offline-conversion
	// import expects, in its vocabulary rather than in the database's: the
	// operator is going to hand this file over without editing it.
	_ = rows.Write([]string{
		"order_id", "click_id", "click_source", "conversion_time", "state",
		"currency", "value_minor", "refunded_minor",
		"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term",
	})
	for _, conversion := range conversions {
		_ = rows.Write([]string{
			conversion.OrderID, conversion.ClickID, conversion.ClickSource,
			conversion.PaidAt.UTC().Format("2006-01-02T15:04:05Z"),
			conversion.State, conversion.Currency,
			strconv.FormatInt(conversion.PaidMinor, 10),
			strconv.FormatInt(conversion.RefundedMinor, 10),
			conversion.Source, conversion.Medium, conversion.Campaign,
			conversion.Content, conversion.Term,
		})
	}
	rows.Flush()
}
