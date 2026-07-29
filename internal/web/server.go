package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"journalol/internal/store"
)

//go:embed templates/*.html templates/partials/*.html static/*
var assets embed.FS

type Server struct {
	store        *store.Store
	templates    *template.Template
	location     *time.Location
	allowedHosts map[string]struct{}
	logger       *slog.Logger
}

func NewServer(
	dataStore *store.Store,
	location *time.Location,
	allowedHosts map[string]struct{},
	logger *slog.Logger,
) (*Server, error) {
	templates, err := template.New("journalol").Funcs(template.FuncMap{
		"roleLabel": roleLabel,
	}).ParseFS(
		assets,
		"templates/*.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		store:        dataStore,
		templates:    templates,
		location:     location,
		allowedHosts: allowedHosts,
		logger:       logger,
	}, nil
}

func roleLabel(role string) string {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "TOP":
		return "Top"
	case "JUNGLE":
		return "Jungle"
	case "MIDDLE":
		return "Mid"
	case "BOTTOM":
		return "Bot"
	case "UTILITY", "SUPPORT":
		return "Support"
	case "":
		return "Unknown role"
	default:
		return role
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /matches", s.matches)
	mux.HandleFunc("GET /matches/{id}", s.matchDetail)
	mux.HandleFunc("POST /matches/{id}/review", s.saveReview)
	mux.HandleFunc("GET /training", s.training)
	mux.HandleFunc("POST /training", s.createTrainingBlock)
	mux.HandleFunc("POST /training/{id}/activate", s.activateTrainingBlock)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(fmt.Sprintf("open embedded static assets: %v", err))
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	var handler http.Handler = mux
	handler = csrfProtection(handler)
	handler = hostGuard(s.allowedHosts, handler)
	handler = s.recoverPanics(handler)
	handler = s.logRequests(handler)
	handler = securityHeaders(handler)
	return handler
}
