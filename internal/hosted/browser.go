package hosted

import (
	"fmt"
	"html"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

func nowUTC() time.Time { return time.Now().UTC() }

func OpenBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}

func writeCallbackPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html><body style=\"font-family:sans-serif;padding:2rem\"><p>%s</p></body></html>", html.EscapeString(msg))
}
