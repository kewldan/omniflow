package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountBackups lets the panel take, restore, delete, and upload backups.
// Taking and restoring need pg_dump and pg_restore, which only the worker
// image carries, so those are recorded as requests the worker carries out.
func (handlers *AdminHandlers) mountBackups(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/system/backups/requests", handlers.backupRequests)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).Group(func(write chi.Router) {
		write.Post("/system/backups/requests", handlers.requestBackupAction)
		if handlers.backups != nil {
			write.Post("/system/backups/upload", handlers.uploadBackup)
		}
	})
}

func (handlers *AdminHandlers) backupRequests(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.BackupRequests(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) requestBackupAction(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.BackupRequestInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	created, err := handlers.operations.RequestBackupAction(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, created, err)
}

func (handlers *AdminHandlers) uploadBackup(writer http.ResponseWriter, request *http.Request) {
	if !handlers.backups.Enabled() {
		writeProblem(writer, request, http.StatusServiceUnavailable, "backups_unavailable",
			"APP_BACKUP_ENCRYPTION_KEY is not set on the api")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, backup.MaxUpload)
	source := request.Body
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		file, _, err := request.FormFile("file")
		if err != nil {
			writeProblem(writer, request, http.StatusUnprocessableEntity, "file_required", "Attach the backup file")
			return
		}
		defer file.Close()
		source = file
	}
	actor := actorFrom(request)
	record, err := handlers.backups.Import(request.Context(), source, "panel:"+actor.AdminID)
	if errors.Is(err, backup.ErrUnreadableUpload) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "backup_unreadable", err.Error())
		return
	}
	if err != nil {
		handlers.logger.Error("backup upload failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "backup_upload_failed", "The file could not be stored")
		return
	}
	_ = handlers.operations.RecordAction(request.Context(), "system.backup.uploaded", "system", "backup",
		uuidString(record.ID), map[string]any{"sizeBytes": record.SizeBytes}, actor)
	writeJSON(writer, http.StatusCreated, map[string]any{"id": uuidString(record.ID), "fileName": record.FileName})
}
