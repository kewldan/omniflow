package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountNews mounts the news editor: news is marketing's, like campaigns.
func (handlers *AdminHandlers) mountNews(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/marketing/news", handlers.listNews)
		read.Get("/marketing/news/{postID}", handlers.getNews)
		read.Get("/marketing/news-categories", handlers.newsCategories)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Post("/marketing/news", handlers.createNews)
		write.Put("/marketing/news/{postID}", handlers.updateNews)
		write.Post("/marketing/news/{postID}/state", handlers.setNewsState)
		write.Put("/marketing/news-categories", handlers.saveNewsCategory)
		write.Delete("/marketing/news-categories/{code}", handlers.deleteNewsCategory)
	})
}

func (handlers *AdminHandlers) newsCategories(writer http.ResponseWriter, request *http.Request) {
	categories, err := handlers.operations.NewsCategories(request.Context())
	handlers.respond(writer, request, map[string]any{"items": categories}, err)
}

func (handlers *AdminHandlers) saveNewsCategory(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.NewsCategory
	if !decodeJSON(writer, request, &body) {
		return
	}
	category, err := handlers.operations.SaveNewsCategory(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, category, err)
}

func (handlers *AdminHandlers) deleteNewsCategory(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteNewsCategory(request.Context(), chi.URLParam(request, "code"), actorFrom(request))
	handlers.respond(writer, request, map[string]bool{"deleted": err == nil}, err)
}

func (handlers *AdminHandlers) listNews(writer http.ResponseWriter, request *http.Request) {
	posts, err := handlers.operations.NewsPosts(request.Context(), request.URL.Query().Get("status"))
	handlers.respond(writer, request, map[string]any{"items": posts}, err)
}

func (handlers *AdminHandlers) getNews(writer http.ResponseWriter, request *http.Request) {
	post, err := handlers.operations.NewsPost(request.Context(), chi.URLParam(request, "postID"))
	handlers.respond(writer, request, post, err)
}

func (handlers *AdminHandlers) createNews(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.NewsInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	post, err := handlers.operations.SaveNewsPost(request.Context(), "", body, actorFrom(request))
	handlers.respond(writer, request, post, err)
}

func (handlers *AdminHandlers) updateNews(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.NewsInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	post, err := handlers.operations.SaveNewsPost(
		request.Context(), chi.URLParam(request, "postID"), body, actorFrom(request))
	handlers.respond(writer, request, post, err)
}

func (handlers *AdminHandlers) setNewsState(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Status       string     `json:"status"`
		ScheduledFor *time.Time `json:"scheduledFor"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	post, err := handlers.operations.SetNewsState(
		request.Context(), chi.URLParam(request, "postID"), body.Status, body.ScheduledFor, actorFrom(request))
	handlers.respond(writer, request, post, err)
}
