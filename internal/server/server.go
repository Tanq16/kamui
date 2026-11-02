package server

import (
	"context"
	"embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Tanq16/kamui/internal/blocklist"
	"github.com/Tanq16/kamui/internal/cache"
	"github.com/Tanq16/kamui/internal/config"
	"github.com/Tanq16/kamui/internal/filter"
	"github.com/Tanq16/kamui/internal/store"
	"github.com/Tanq16/kamui/internal/upstream"
)

//go:embed static
var staticFiles embed.FS

const maxBody = 8 << 20

type Deps struct {
	Version  string
	Config   *config.Manager
	Lists    *blocklist.Manager
	Filter   *filter.Filter
	Cache    *cache.Cache
	Upstream *upstream.Resolver
	Store    *store.Store
}

type Server struct {
	ctx  context.Context
	addr string
	d    Deps
	mux  *http.ServeMux
	srv  *http.Server
}

func New(ctx context.Context, addr string, d Deps) *Server {
	s := &Server{ctx: ctx, addr: addr, d: d, mux: http.NewServeMux()}
	s.srv = &http.Server{Addr: addr, Handler: s.knownHost(s.mux), ReadHeaderTimeout: 10 * time.Second}
	return s
}

func (s *Server) Setup() error {
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	s.mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFileFS(w, r, staticFS, "sw.js")
	})
	s.mux.HandleFunc("/", s.handleIndex)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
	}))
	mutate := func(h http.HandlerFunc) http.Handler {
		return cop.Handler(requireJSON(h))
	}

	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.Handle("PUT /api/blocking", mutate(s.handleBlocking))
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/querylog", s.handleQueryLog)
	s.mux.HandleFunc("GET /api/querylog/export", s.handleQueryLogExport)

	s.mux.HandleFunc("GET /api/lists", s.handleLists)
	s.mux.Handle("POST /api/lists", mutate(s.handleListAdd))
	s.mux.Handle("PATCH /api/lists/{id}", mutate(s.handleListPatch))
	s.mux.Handle("DELETE /api/lists/{id}", mutate(s.handleListDelete))
	s.mux.Handle("POST /api/lists/refresh", mutate(s.handleListsRefresh))
	s.mux.Handle("POST /api/lists/{id}/refresh", mutate(s.handleListRefresh))

	s.mux.HandleFunc("GET /api/rules/{kind}", s.handleRules)
	s.mux.Handle("POST /api/rules/{kind}", mutate(s.handleRuleAdd))
	s.mux.Handle("DELETE /api/rules/{kind}/{id}", mutate(s.handleRuleDelete))

	s.mux.HandleFunc("GET /api/rewrites", s.handleRewrites)
	s.mux.Handle("POST /api/rewrites", mutate(s.handleRewriteAdd))
	s.mux.Handle("DELETE /api/rewrites/{id}", mutate(s.handleRewriteDelete))

	s.mux.HandleFunc("GET /api/test", s.handleTest)
	s.mux.HandleFunc("GET /api/settings", s.handleSettings)
	s.mux.Handle("PUT /api/settings", mutate(s.handleSettingsPut))
	s.mux.Handle("POST /api/cache/flush", mutate(s.handleCacheFlush))
	s.mux.HandleFunc("GET /api/config/export", s.handleExport)
	s.mux.Handle("POST /api/config/import", mutate(s.handleImport))
	return nil
}

func (s *Server) Run() error {
	log.Info().Str("addr", s.addr).Msg("starting web server")
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) knownHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			writeError(w, http.StatusForbidden, "unknown host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	host = filter.Normalize(host)
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "localhost" {
		return true
	}
	if _, ok := s.d.Filter.Rewrite(host); ok {
		return true
	}
	_, ok := s.d.Filter.LocalDomain(host)
	return ok
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func requireJSON(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "request body must be application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, v); err != nil {
		log.Error().Err(err).Msg("failed to encode response")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.UnmarshalRead(r.Body, v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}
