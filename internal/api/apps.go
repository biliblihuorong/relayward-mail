package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"relayward-mail/internal/store"
)

// defaultRatePerHour matches the plan's default when a create request omits
// the rate.
const defaultRatePerHour = 500

// appJSON is the wire form of an app; the password hash is never exposed.
type appJSON struct {
	Name          string    `json:"name"`
	DisplayName   string    `json:"display_name"`
	Enabled       bool      `json:"enabled"`
	Unsubscribe   bool      `json:"unsubscribe"`
	BodyInjection bool      `json:"body_injection"`
	AllowedFrom   []string  `json:"allowed_from"`
	RatePerHour   int       `json:"rate_per_hour"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toAppJSON(a *store.App) appJSON {
	return appJSON{
		Name:          a.Name,
		DisplayName:   a.DisplayName,
		Enabled:       a.Enabled,
		Unsubscribe:   a.Unsubscribe,
		BodyInjection: a.BodyInjection,
		AllowedFrom:   a.AllowedFrom,
		RatePerHour:   a.RatePerHour,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

// createAppRequest is the POST /api/apps body.
type createAppRequest struct {
	Name          string   `json:"name"`
	AllowedFrom   []string `json:"allowed_from"`
	RatePerHour   *int     `json:"rate_per_hour"`
	DisplayName   string   `json:"display_name"`
	Unsubscribe   *bool    `json:"unsubscribe"`
	BodyInjection *bool    `json:"body_injection"`
}

// handleListApps serves GET /api/apps.
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApps(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	out := make([]appJSON, 0, len(apps))
	for i := range apps {
		out = append(out, toAppJSON(&apps[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

// handleGetApp serves GET /api/apps/{name}: configuration plus stats.
func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	app, err := s.store.GetAppByName(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, codeAppNotFound, "app not found")
		return
	}
	stats, err := s.store.AppStatsByName(r.Context(), name)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": toAppJSON(app), "stats": stats})
}

// handleCreateApp serves POST /api/apps. The SMTP password is returned once.
func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.AllowedFrom) == 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "name and allowed_from are required")
		return
	}
	rate := defaultRatePerHour
	if req.RatePerHour != nil {
		if *req.RatePerHour <= 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "rate_per_hour must be positive")
			return
		}
		rate = *req.RatePerHour
	}
	unsubscribe := true
	if req.Unsubscribe != nil {
		unsubscribe = *req.Unsubscribe
	}
	bodyInjection := true
	if req.BodyInjection != nil {
		bodyInjection = *req.BodyInjection
	}

	password, err := store.RandomPassword()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	hash, err := store.HashPassword(password)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	app := &store.App{
		Name:          req.Name,
		PasswordHash:  hash,
		Enabled:       true,
		Unsubscribe:   unsubscribe,
		BodyInjection: bodyInjection,
		AllowedFrom:   req.AllowedFrom,
		RatePerHour:   rate,
		DisplayName:   req.DisplayName,
	}
	if err := s.store.CreateApp(r.Context(), app, s.auditEntry(r, store.ActionAppCreate, req.Name, "")); err != nil {
		s.writeAppError(w, err)
		return
	}
	if s.limiter != nil {
		s.limiter.Update(app.ID, app.RatePerHour)
	}

	s.logger.Info("app created",
		slog.String("app", app.Name),
		slog.String("action", store.ActionAppCreate))
	writeJSON(w, http.StatusCreated, map[string]any{"app": toAppJSON(app), "smtp_password": password})
}

// appPatchRequest is the PATCH /api/apps/{name} body; nil fields stay
// unchanged.
type appPatchRequest struct {
	Enabled       *bool    `json:"enabled"`
	Unsubscribe   *bool    `json:"unsubscribe"`
	BodyInjection *bool    `json:"body_injection"`
	RatePerHour   *int     `json:"rate_per_hour"`
	AllowedFrom   []string `json:"allowed_from"`
	DisplayName   *string  `json:"display_name"`
}

// handlePatchApp serves PATCH /api/apps/{name}.
func (s *Server) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req appPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RatePerHour != nil && *req.RatePerHour <= 0 {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "rate_per_hour must be positive")
		return
	}

	upd := store.AppUpdate{
		Enabled:       req.Enabled,
		Unsubscribe:   req.Unsubscribe,
		BodyInjection: req.BodyInjection,
		RatePerHour:   req.RatePerHour,
		AllowedFrom:   req.AllowedFrom,
		DisplayName:   req.DisplayName,
	}
	app, err := s.store.UpdateApp(r.Context(), name, upd, s.auditEntry(r, store.ActionAppUpdate, name, ""))
	if err != nil {
		s.writeAppError(w, err)
		return
	}
	// Only a changed rate replaces the bucket; other edits must not hand the
	// app a fresh full quota.
	if s.limiter != nil && req.RatePerHour != nil {
		s.limiter.Update(app.ID, app.RatePerHour)
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": toAppJSON(app)})
}

// handleRotateApp serves POST /api/apps/{name}/rotate: the new SMTP password
// is returned once and the old one stops working immediately.
func (s *Server) handleRotateApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	password, err := store.RandomPassword()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	hash, err := store.HashPassword(password)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.store.RotateAppPassword(r.Context(), name, hash, s.auditEntry(r, store.ActionAppRotate, name, "")); err != nil {
		s.writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"smtp_password": password})
}

// handleDeleteApp serves DELETE /api/apps/{name}. Message logs are kept.
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	app, err := s.store.GetAppByName(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, codeAppNotFound, "app not found")
		return
	}
	if err := s.store.SoftDeleteApp(r.Context(), name, s.auditEntry(r, store.ActionAppDelete, name, "")); err != nil {
		s.writeAppError(w, err)
		return
	}
	if s.limiter != nil {
		s.limiter.Remove(app.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAppError maps store errors on app routes to their specific codes.
func (s *Server) writeAppError(w http.ResponseWriter, err error) {
	switch {
	case isStoreNotFound(err):
		writeError(w, http.StatusNotFound, codeAppNotFound, "app not found")
	case isStoreConflict(err):
		writeError(w, http.StatusConflict, codeAppExists, "app name already exists")
	case isStoreInvalid(err):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, invalidMessage(err))
	default:
		s.writeStoreError(w, err)
	}
}

func isStoreNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
func isStoreConflict(err error) bool { return errors.Is(err, store.ErrConflict) }
func isStoreInvalid(err error) bool  { return errors.Is(err, store.ErrInvalidInput) }

func invalidMessage(err error) string {
	if len(err.Error()) > 120 {
		return err.Error()[:120]
	}
	return err.Error()
}
