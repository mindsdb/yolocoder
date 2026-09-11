package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
)

// errorShim is injected into every HTML page served through the proxy, so
// that a browser-side crash in the app being built reaches the chat the
// same way a server-side one does, without the app's own source needing to
// know anything about yolocoder. It reports to the parent window (this
// page, not the iframe) via postMessage, which app.js listens for.
const errorShim = `<script>(function(){
window.addEventListener("error", function(event){
  window.parent.postMessage({type:"APP_RUNTIME_ERROR",error:{message:event.message,filename:event.filename,lineno:event.lineno,colno:event.colno,stack:event.error&&event.error.stack}},"*");
});
window.addEventListener("unhandledrejection", function(event){
  var reason=event.reason;
  window.parent.postMessage({type:"APP_UNHANDLED_REJECTION",error:{message:String(reason&&reason.message||reason),stack:reason&&reason.stack}},"*");
});
})();</script>`

// newAppProxy reverse-proxies /app/ onto the dev server's own port, which
// port learns from portFn each request since a restart can change it. It
// rewrites HTML responses to inject the shim above and otherwise passes
// everything through untouched, including WebSocket upgrades: Vite's own
// HMR socket needs to reach the real dev server for hot reload to work,
// and httputil.ReverseProxy forwards a hijacked connection as-is.
func newAppProxy(portFn func() int) http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			port := portFn()
			request.URL.Scheme = "http"
			request.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
			request.URL.Path = strings.TrimPrefix(request.URL.Path, "/app")
			if request.URL.Path == "" {
				request.URL.Path = "/"
			}
			// The proxy rewrites the body of an HTML response, so it must
			// arrive uncompressed to rewrite; a real client's own
			// Accept-Encoding would otherwise get a gzip stream back.
			request.Header.Del("Accept-Encoding")
		},
		ModifyResponse: injectShim,
		ErrorHandler: func(response http.ResponseWriter, request *http.Request, err error) {
			http.Error(response, "dev server is not reachable yet: "+err.Error(), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if portFn() == 0 {
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			response.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(response, "<p>The dev server isn't running. Use Start in the sidebar.</p>")
			return
		}
		proxy.ServeHTTP(response, request)
	})
}

func injectShim(response *http.Response) error {
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	injected := insertShim(body)
	response.Body = io.NopCloser(bytes.NewReader(injected))
	response.ContentLength = int64(len(injected))
	response.Header.Set("Content-Length", strconv.Itoa(len(injected)))
	return nil
}

// insertShim places the shim right after <head>, or at the very top of a
// page that has no head tag at all (an HTML fragment, or a hand-rolled
// page without one).
func insertShim(html []byte) []byte {
	marker := []byte("<head>")
	index := bytes.Index(html, marker)
	if index == -1 {
		return append([]byte(errorShim), html...)
	}
	out := make([]byte, 0, len(html)+len(errorShim))
	out = append(out, html[:index+len(marker)]...)
	out = append(out, []byte(errorShim)...)
	out = append(out, html[index+len(marker):]...)
	return out
}
