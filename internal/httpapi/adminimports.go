package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/rbac"
)

// maxImportFile bounds an uploaded customer CSV.
const maxImportFile = 10 << 20

// mountImports lets the panel run a customer import end to end: preview from
// Remnawave page by page, or stage an uploaded CSV, then apply what is valid.
// They used to be bearer-only, so the panel could list imports it could not
// start or finish.
func (handlers *AdminHandlers) mountImports(secure chi.Router) {
	if handlers.imports == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCustomersWrite)).Group(func(write chi.Router) {
		write.Post("/imports/remnawave/preview", handlers.previewRemnawaveImport)
		write.Post("/imports/file", handlers.uploadImportFile)
		write.Post("/imports/{importID}/apply", handlers.applyCustomerImport)
	})
}

type importView struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	Total     int32     `json:"total"`
	Valid     int32     `json:"valid"`
	Conflicts int32     `json:"conflicts"`
	Invalid   int32     `json:"invalid"`
	StartedAt time.Time `json:"startedAt"`
}

func importViewFrom(run dbgen.CustomerImport) importView {
	return importView{
		ID: uuidString(run.ID), Source: run.Source, Status: run.Status, Total: run.TotalCount,
		Valid: run.ValidCount, Conflicts: run.ConflictCount, Invalid: run.InvalidCount,
		StartedAt: run.StartedAt.Time,
	}
}

func (handlers *AdminHandlers) auditImport(request *http.Request, action, importID string, metadata map[string]any) {
	principal, _ := PrincipalFrom(request.Context())
	_ = handlers.service.AppendAudit(request.Context(), adminauthpg.AuditEntry{
		ActorType: "admin", ActorID: principal.Account.ID, Action: action, Category: "customers",
		Outcome: "success", TargetType: "customer_import", TargetID: importID,
		RequestID: middleware.GetReqID(request.Context()), Metadata: metadata,
	})
}

func (handlers *AdminHandlers) previewRemnawaveImport(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ImportID string `json:"importId"`
		PageSize int    `json:"pageSize"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	run, err := handlers.imports.Preview(request.Context(), body.ImportID, body.PageSize)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "import_failed", "The preview could not run")
		return
	}
	handlers.auditImport(request, "customer.import.previewed", uuidString(run.ID), map[string]any{"source": run.Source, "status": run.Status})
	writeJSON(writer, http.StatusOK, importViewFrom(run))
}

func (handlers *AdminHandlers) uploadImportFile(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxImportFile)
	var source io.Reader = request.Body
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		file, _, err := request.FormFile("file")
		if err != nil {
			writeProblem(writer, request, http.StatusUnprocessableEntity, "file_required", "Attach a CSV file")
			return
		}
		defer file.Close()
		source = file
	}
	rows, invalid, err := importservice.ParseFile(source)
	if err != nil {
		detail := "The file is not a customer import CSV"
		if errors.Is(err, importservice.ErrFileFormat) {
			detail = err.Error()
		}
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_import_file", detail)
		return
	}
	run, err := handlers.imports.PreviewFile(request.Context(), rows, invalid)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "import_failed", "The file could not be staged")
		return
	}
	handlers.auditImport(request, "customer.import.uploaded", uuidString(run.ID), map[string]any{"rows": run.TotalCount})
	writeJSON(writer, http.StatusOK, importViewFrom(run))
}

func (handlers *AdminHandlers) applyCustomerImport(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Limit int `json:"limit"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	run, err := handlers.imports.Apply(request.Context(), chi.URLParam(request, "importID"), body.Limit)
	if err != nil {
		writeProblem(writer, request, http.StatusConflict, "import_not_ready", "The import is not ready to apply")
		return
	}
	handlers.auditImport(request, "customer.import.applied", uuidString(run.ID), map[string]any{"status": run.Status})
	writeJSON(writer, http.StatusOK, importViewFrom(run))
}
