package version

var (
	Version = "dev"
	Commit  = ""
)

func Display() string {
	if Commit == "" {
		return Version
	}
	short := Commit
	if len(short) > 7 {
		short = short[:7]
	}
	return Version + " (" + short + ")"
}

// UserAgent names yolocoder on every request it makes. Without one, Go
// sends "Go-http-client/1.1", which MindsHub's Cloudflare rules refuse on
// /v1/decisions with a 403 page: every decision-model call had been
// failing that way, silently, since each falls back to doing without.
func UserAgent() string {
	agent := "yolocoder/" + Version
	if Commit != "" {
		short := Commit
		if len(short) > 7 {
			short = short[:7]
		}
		agent += "+" + short
	}
	return agent
}
