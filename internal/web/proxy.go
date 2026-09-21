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
//
// window "error" and "unhandledrejection" alone miss the errors a person
// actually sees while using the app: React logs a crashed render through
// console.error (an error boundary catches it, so nothing ever throws to
// the window), and Vite's overlay reports a failed import the same way.
// The console.error wrapper forwards only what looks like a real error —
// an Error object, or a string shaped like one — so ordinary logging,
// HMR chatter and deprecation warnings never become chat cards.
const errorShim = `<script>(function(){
function isDiagnostic(text){
  return /(?:\[violation\]|canvas2d:|willreadfrequently|requestanimationframe(?: handler)? took|forced reflow|long task)/i.test(String(text||""));
}
function report(type,err,extra){
  var message="",stack="";
  if(err&&err.stack){message=String(err.message||err);stack=String(err.stack);}
  else{message=String((err&&err.message)||err);stack=(err&&err.stack)||"";}
  if(!message||isDiagnostic(message)||isDiagnostic(stack)){return;}
  var payload={message:message,stack:String(stack)};
  if(extra){for(var key in extra){payload[key]=extra[key];}}
  window.parent.postMessage({type:type,error:payload},"*");
}
window.addEventListener("error", function(event){
  if(event.message||event.error){report("APP_RUNTIME_ERROR",event.error||event.message,{filename:event.filename,lineno:event.lineno,colno:event.colno});}
});
window.addEventListener("unhandledrejection", function(event){
  report("APP_UNHANDLED_REJECTION",event.reason);
});
var origError=console.error.bind(console);
console.error=function(){
  try{
    for(var i=0;i<arguments.length;i++){
      var arg=arguments[i];
      if(arg instanceof Error){report("APP_CONSOLE_ERROR",arg);break;}
      if(typeof arg==="string"&&/[A-Za-z]*Error:/.test(arg)){report("APP_CONSOLE_ERROR",arg);break;}
    }
  }catch(e){}
  return origError.apply(null,arguments);
};
})();</script>`

// selfHealingPage is served in place of the app whenever the dev server
// can't be reached at all — before recovery even has a chance to run, or
// while it's in progress. It polls its own URL every couple of seconds
// and reloads the moment that comes back with a real response, so the
// iframe recovers on its own the instant the dev server does, rather
// than sitting on a stale error until someone manually refreshes the
// browser. Same-origin (it's fetching itself, through this same proxy),
// so none of this trips over CORS the way probing from the parent page
// would.
const selfHealingPage = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Reconnecting…</title></head>
<body style="font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:#666;background:#fff;padding:2rem;line-height:1.6">
<p>%s</p>
<p id="retry-status" style="color:#999;font-size:0.85em">Checking again every couple of seconds — this reloads on its own once it's back.</p>
<script>(function poll(attempt){
  var maxAttempts=60;
  if(attempt>=maxAttempts){
    document.getElementById("retry-status").textContent="The dev server is still unavailable. Reload this preview to try again.";
    return;
  }
  fetch(location.href,{cache:"no-store"}).then(function(response){
    if(response.headers.get("X-YoloCoder-Reconnecting")!=="true"){
      location.reload();
      return;
    }
    var delay=Math.min(10000,1000+attempt*250);
    setTimeout(function(){poll(attempt+1);},delay);
  },function(){
    var delay=Math.min(10000,1000+attempt*250);
    setTimeout(function(){poll(attempt+1);},delay);
  });
})(0);</script>
</body>
</html>`

func writeSelfHealingPage(response http.ResponseWriter, message string) {
	// This is a deliberate recovery document, not an application response.
	// Returning 200 avoids a red 502 in DevTools during the normal few
	// seconds when Vite or the supervised process is restarting. The private
	// header lets the page distinguish itself from the real app on each poll.
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("X-YoloCoder-Reconnecting", "true")
	response.WriteHeader(http.StatusOK)
	fmt.Fprintf(response, selfHealingPage, message)
}

// newAppProxy reverse-proxies onto the dev server's own port, which it
// learns from portFn each request since a restart can change it. It's
// mounted at the root of its own dedicated listener (see appProxyPort in
// server.go) rather than under a path prefix like /app/ on the main UI's
// server: a dev server's own absolute asset paths (Vite's /src/main.tsx,
// /@vite/client, and so on) are written assuming they own the whole
// origin, and resolve against the wrong place — the main UI's origin, not
// the proxy's — if loaded from under a sub-path instead.
//
// It rewrites HTML responses to inject the shim above and otherwise
// passes everything through untouched, including WebSocket upgrades:
// Vite's own HMR socket needs to reach the real dev server for hot reload
// to work, and httputil.ReverseProxy forwards a hijacked connection as-is.
func newAppProxy(portFn func() int) http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(request *http.Request) {
			port := portFn()
			request.URL.Scheme = "http"
			request.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
			// The proxy rewrites the body of an HTML response, so it must
			// arrive uncompressed to rewrite; a real client's own
			// Accept-Encoding would otherwise get a gzip stream back.
			request.Header.Del("Accept-Encoding")
		},
		// The dev server this points at can restart (a new process on the
		// same port) or crash entirely at any moment, which a pooled
		// keep-alive connection has no way to know about: it looks alive
		// right up until it's used again and gets back a stray response
		// or a reset, logged by net/http's transport as an "Unsolicited
		// response ... 400 Bad Request" that has nothing to do with an
		// actual HTTP 400 anywhere. A dial per request costs nothing
		// meaningful over loopback and avoids that whole class of stale
		// connection confusingly reported.
		Transport:      &http.Transport{DisableKeepAlives: true},
		ModifyResponse: injectShim,
		ErrorHandler: func(response http.ResponseWriter, request *http.Request, err error) {
			writeSelfHealingPage(response, "dev server is not reachable yet: "+err.Error())
		},
		// ErrorHandler above only runs for a failure before any response
		// has gone out; one that happens partway through streaming a
		// response body (the dev server dying or restarting mid-request)
		// can't be turned into an HTTP error anymore, so ReverseProxy
		// logs it directly instead — through this rather than an
		// unlabeled log.Default() straight to stderr.
		ErrorLog: httpErrorLog,
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if portFn() == 0 {
			writeSelfHealingPage(response, "The dev server isn't running yet.")
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
