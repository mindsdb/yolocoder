package webfe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// Turn is one exchange as the browser keeps it and hands back: what was
// asked, what came of it, and which files it touched. It is the whole of
// the history the agent sees, so it is signed along with the files.
type Turn struct {
	Message string   `json:"message"`
	Summary string   `json:"summary"`
	Files   []string `json:"files,omitempty"`
}

// signer attests that a project — its files and its history together —
// is one this server handed out, either a starter or the result of a
// turn. The browser holds the project and sends it back with every turn,
// so without this anyone could put anything in front of the model, and
// the model in front of anything; with it, the only thing a person
// steers is the message they type.
//
// There is no expiry and no user in it. Nobody signs in, and a project
// left in a browser for a week should still open. Changing the key is
// how every outstanding project is retired at once.
type signer struct {
	key []byte
}

// stateVersion is part of what is signed, so a change to what a state
// means can retire every old one without changing the key.
const stateVersion = 1

func (signer signer) sign(files map[string]string, history []Turn) string {
	if history == nil {
		history = []Turn{}
	}
	// encoding/json writes map keys sorted, so the same project always
	// encodes to the same bytes, whatever order it arrived in.
	canonical, _ := json.Marshal(struct {
		Version int               `json:"v"`
		Files   map[string]string `json:"files"`
		History []Turn            `json:"history"`
	}{stateVersion, files, history})
	mac := hmac.New(sha256.New, signer.key)
	mac.Write(canonical)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (signer signer) verify(files map[string]string, history []Turn, state string) bool {
	return hmac.Equal([]byte(signer.sign(files, history)), []byte(state))
}
