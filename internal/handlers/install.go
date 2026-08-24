// Package handlers translates HTTP into service calls, and their results back
// into HTTP.
//
// There are no business rules here, and not one query. If a handler starts
// deciding something, that decision is in the wrong place.
package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/services"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// stateCookie carries the OAuth nonce between /install and /auth/callback.
const stateCookie = "oauth_state"

type InstallHandler struct {
	service *services.InstallService
	logger  *slog.Logger
}

func NewInstallHandler(service *services.InstallService, logger *slog.Logger) *InstallHandler {
	return &InstallHandler{service: service, logger: logger}
}

// Begin starts the OAuth flow.
func (h *InstallHandler) Begin(w http.ResponseWriter, r *http.Request) error {
	redirect, state, err := h.service.Begin(r.Context(), r.URL.Query().Get("shop"), r.URL.RawQuery)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:  stateCookie,
		Value: state,
		Path:  "/",
		// HttpOnly: no script on any page needs to read this.
		// Secure: a Shopify app's address must be HTTPS, so there is no reason
		// to send it over a plain connection.
		// Lax: this cookie has to come along when Shopify redirects back here,
		// and that redirect arrives from another domain.
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})

	http.Redirect(w, r, redirect, http.StatusFound)
	return nil
}

// Callback finishes the OAuth flow.
func (h *InstallHandler) Callback(w http.ResponseWriter, r *http.Request) error {
	var state string
	if c, err := r.Cookie(stateCookie); err == nil {
		state = c.Value
	}

	shop, err := h.service.Complete(r.Context(), r.URL.Query(), r.URL.RawQuery, state)
	if err != nil {
		return err
	}

	// The cookie is discarded the moment it has been used. A nonce still
	// sitting there after the install is done is only waiting to be reused.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})

	httpx.OK(w, "App installed.", shop)
	return nil
}
