package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountReports registers the reporting surfaces.
//
// They sit behind `finance.read` rather than a permission of their own. A sales
// report is money, and an installation that has decided who may read money has
// already made this decision — a second permission would let the two drift into
// disagreeing about the same figures.
func (handlers *AdminHandlers) mountReports(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/reports/sales", handlers.salesReport)
		read.Get("/reports/sales/export", handlers.exportSalesReport)
	})
}

// reportPeriod reads the period from the query, defaulting to the last thirty
// days and to the installation's timezone.
func reportPeriod(request *http.Request) (since, until time.Time, timezone, currency string) {
	now := time.Now().UTC()
	until = now
	if parsed := queryTime(request, "until"); parsed != nil {
		until = *parsed
	}
	// The same thirty days the dashboard reports, so the first thing an
	// operator sees here reconciles with the first thing they saw there.
	since = until.Add(-defaultReportWindow)
	if parsed := queryTime(request, "since"); parsed != nil {
		since = *parsed
	}
	timezone = query(request, "timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	return since, until, timezone, strings.ToUpper(query(request, "currency"))
}

func (handlers *AdminHandlers) salesReport(writer http.ResponseWriter, request *http.Request) {
	since, until, timezone, currency := reportPeriod(request)
	report, err := handlers.operations.SalesReport(
		request.Context(), since, until, timezone, currency)
	handlers.respond(writer, request, report, err)
}

// exportSalesReport writes the same report as CSV.
//
// It is one file with a `section` column rather than four files, because the
// four breakdowns are four views of one period and an operator opening a
// spreadsheet wants the period, not a folder. The header is stable, which is
// what makes it usable from a script.
func (handlers *AdminHandlers) exportSalesReport(writer http.ResponseWriter, request *http.Request) {
	since, until, timezone, currency := reportPeriod(request)
	report, err := handlers.operations.SalesReport(
		request.Context(), since, until, timezone, currency)
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}

	writer.Header().Set("Content-Type", "text/csv; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="sales.csv"`)
	writer.WriteHeader(http.StatusOK)

	write := func(fields ...string) {
		encoded := make([]string, 0, len(fields))
		for _, field := range fields {
			encoded = append(encoded, csvField(field))
		}
		_, _ = writer.Write([]byte(strings.Join(encoded, ",") + "\n"))
	}

	// Amounts are exported in minor units, unscaled, exactly as they are
	// stored. A spreadsheet that divided by a hundred would be wrong for every
	// zero-decimal currency, and dividing is something the reader can do
	// knowing their own currency.
	write("section", "key", "detail", "currency", "orders", "subtotal_minor",
		"discount_minor", "paid_minor", "wallet_minor")

	for _, line := range report.ByOperation {
		write("operation", line.Operation, "", line.Currency,
			strconv.FormatInt(line.Orders, 10),
			strconv.FormatInt(line.SubtotalMinor, 10),
			strconv.FormatInt(line.DiscountMinor, 10),
			strconv.FormatInt(line.PaidMinor, 10),
			strconv.FormatInt(line.WalletMinor, 10))
	}
	for _, line := range report.ByPlan {
		write("plan", line.PlanCode,
			"v"+strconv.FormatInt(int64(line.PlanVersion), 10)+" "+line.BillingPeriod,
			line.Currency, strconv.FormatInt(line.Orders, 10),
			strconv.FormatInt(line.GrossMinor, 10), "", "", "")
	}
	for _, line := range report.ByDay {
		write("day", line.Day, "", line.Currency,
			strconv.FormatInt(line.Orders, 10), "", "",
			strconv.FormatInt(line.PaidMinor, 10),
			strconv.FormatInt(line.WalletMinor, 10))
	}
	for _, line := range report.Refunds {
		write("refund", "", "", line.Currency,
			strconv.FormatInt(line.Refunds, 10), "", "",
			strconv.FormatInt(line.RefundedMinor, 10), "")
	}
	write("trial", "claimed", "", "", strconv.FormatInt(report.Trials.Trials, 10), "", "", "", "")
	write("trial", "converted", "", "", strconv.FormatInt(report.Trials.Converted, 10), "", "", "", "")
}
