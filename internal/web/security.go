package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const (
	csrfCookieName = "journalol_csrf"
	maxFormBytes   = 1 << 20
)

type csrfContextKey struct{}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func hostGuard(allowed map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHostname(r.Host)
		if _, ok := allowed[strings.ToLower(host)]; !ok {
			http.Error(w, "unrecognized host", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestHostname(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(hostport, "[]")
}

func csrfProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		existingCookie, cookieErr := r.Cookie(csrfCookieName)
		replaceCookie := cookieErr == http.ErrNoCookie ||
			(cookieErr == nil && !validCSRFToken(existingCookie.Value))

		token, err := csrfTokenFromRequest(r)
		if err != nil {
			http.Error(w, "could not initialize request security", http.StatusInternalServerError)
			return
		}

		if replaceCookie {
			http.SetCookie(w, &http.Cookie{
				Name:     csrfCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				Secure:   r.TLS != nil,
				SameSite: http.SameSiteStrictMode,
			})
		}

		r = r.WithContext(context.WithValue(r.Context(), csrfContextKey{}, token))

		if isMutation(r.Method) {
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid or oversized form", http.StatusBadRequest)
				return
			}
			submitted := r.PostForm.Get("_csrf")
			if subtle.ConstantTimeCompare([]byte(token), []byte(submitted)) != 1 {
				http.Error(w, "invalid request token", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func csrfTokenFromRequest(r *http.Request) (string, error) {
	cookie, err := r.Cookie(csrfCookieName)
	if err == nil && validCSRFToken(cookie.Value) {
		return cookie.Value, nil
	}
	if err != nil && err != http.ErrNoCookie {
		return "", fmt.Errorf("read CSRF cookie: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate CSRF token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validCSRFToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32
}

func csrfToken(r *http.Request) string {
	token, _ := r.Context().Value(csrfContextKey{}).(string)
	return token
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
