package auth

import (
	"net/http"
	"strings"
)

// Device turns a User-Agent into a short, stable description like
// "Chrome on Windows". Sessions are tied to it, so it must not change when
// the browser updates: only the family and the operating system count.
func Device(userAgent string) string {
	ua := userAgent
	browser := "A browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	case ua == "":
		browser = "Unknown app"
	}
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "Mac"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if os == "" {
		return browser
	}
	return browser + " on " + os
}

// SameOrigin reports whether a browser request came from this site. A
// cookie-bearing request from another origin is a cross-site forgery
// attempt, whatever the cookie's SameSite setting says. Requests without
// browser headers (apps, curl) pass; they can't carry a cookie anyway.
func SameOrigin(h http.Header, host string) bool {
	if site := h.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	origin := h.Get("Origin")
	if origin == "" {
		return true
	}
	_, after, ok := strings.Cut(origin, "://")
	return ok && strings.EqualFold(after, host)
}
