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
