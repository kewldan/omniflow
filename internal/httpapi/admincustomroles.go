package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountCustomRoles lets an operator who manages roles define named
// permission sets. Changing them is admins.roles, the permission that
// already governs who holds which role.
func (handlers *AdminHandlers) mountCustomRoles(secure chi.Router) {
	secure.With(handlers.requirePermission(rbac.PermissionAdminsRoles)).Group(func(write chi.Router) {
		write.Put("/rbac/custom-roles/{role}", handlers.saveCustomRole)
		write.Delete("/rbac/custom-roles/{role}", handlers.deleteCustomRole)
	})
}

func (handlers *AdminHandlers) saveCustomRole(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	permissions := make([]rbac.Permission, 0, len(body.Permissions))
	for _, permission := range body.Permissions {
		permissions = append(permissions, rbac.Permission(permission))
	}
	principal, _ := PrincipalFrom(request.Context())
	err := handlers.service.SaveCustomRole(request.Context(), principal, rbac.CustomRole{
		Role: rbac.Role(chi.URLParam(request, "role")), Name: body.Name, Permissions: permissions,
	}, adminauthpg.RequestContext{RequestID: middleware.GetReqID(request.Context())})
	handlers.customRoleResult(writer, request, err)
}

func (handlers *AdminHandlers) deleteCustomRole(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	err := handlers.service.DeleteCustomRole(request.Context(), principal, chi.URLParam(request, "role"),
		adminauthpg.RequestContext{RequestID: middleware.GetReqID(request.Context())})
	handlers.customRoleResult(writer, request, err)
}

func (handlers *AdminHandlers) customRoleResult(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case err == nil:
		writeJSON(writer, http.StatusOK, map[string]any{"saved": true})
	case errors.Is(err, rbac.ErrInvalidCustomRole):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_custom_role", err.Error())
	case errors.Is(err, adminauthpg.ErrRoleNotHeld):
		writeProblem(writer, request, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, adminauthpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That role was not found")
	default:
		handlers.logger.Error("custom role change failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "custom_role_failed", "The role could not be saved")
	}
}
