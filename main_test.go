package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestRouter() http.Handler {
	s := &server{startedAt: now()}
	return s.routes()
}

func newVoteServer(t *testing.T) (*server, string) {
	t.Helper()
	s := &server{startedAt: now(), votesFile: t.TempDir() + "/results.txt"}
	return s, voterID(t, s.routes())
}

func do(t *testing.T, handler http.Handler, method, path, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w, nil
}

// voterID fetches / once and returns the voter_id cookie value it sets.
func voterID(t *testing.T, h http.Handler) string {
	t.Helper()
	w, _ := do(t, h, http.MethodGet, "/", "")
	for _, c := range w.Result().Cookies() {
		if c.Name == voterCookie {
			return c.Value
		}
	}
	t.Fatalf("no %q cookie set", voterCookie)
	return ""
}

func postVote(t *testing.T, h http.Handler, id, answer string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"answer": {answer}}
	req := httptest.NewRequest(http.MethodPost, "/vote", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id != "" {
		req.AddCookie(&http.Cookie{Name: voterCookie, Value: id})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func readResults(t *testing.T, s *server) string {
	t.Helper()
	b, err := os.ReadFile(s.votesFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	return string(b)
}

func getHome(t *testing.T, h http.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if id != "" {
		req.AddCookie(&http.Cookie{Name: voterCookie, Value: id})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHomeShowsFormWhenNoVote(t *testing.T) {
	w, _ := do(t, newTestRouter(), http.MethodGet, "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Do you condemn Hasan Piker?") {
		t.Fatalf("page missing the question")
	}
	if !strings.Contains(body, `method="post"`) || !strings.Contains(body, `action="/vote"`) {
		t.Fatalf("page missing vote form")
	}
	if !strings.Contains(body, `name="answer" value="yes"`) || !strings.Contains(body, `name="answer" value="no"`) {
		t.Fatalf("page missing yes/no submit buttons")
	}
}

func TestRemovedRoutesReturn404(t *testing.T) {
	for _, path := range []string{"/health", "/api/echo", "/api/users", "/api/users/1", "/yes", "/no"} {
		w, _ := do(t, newTestRouter(), http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, w.Code, http.StatusNotFound)
		}
	}
}

func TestCookieSetOnHome(t *testing.T) {
	w, _ := do(t, newTestRouter(), http.MethodGet, "/", "")
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == voterCookie {
			found = true
			if c.Value == "" {
				t.Fatalf("cookie %q has empty value", voterCookie)
			}
		}
	}
	if !found {
		t.Fatalf("home page did not set %q cookie", voterCookie)
	}
}

func TestVoteRecordsAndRedirects(t *testing.T) {
	s, id := newVoteServer(t)

	w := postVote(t, s.routes(), id, "yes")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("vote status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("redirect location = %q, want /", loc)
	}

	lines := strings.Split(strings.TrimSpace(readResults(t, s)), "\n")
	if len(lines) != 1 {
		t.Fatalf("results has %d lines, want 1", len(lines))
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) != 3 {
		t.Fatalf("line fields = %v, want timestamp\tuuid\tchoice", fields)
	}
	if _, err := time.Parse(time.RFC3339, fields[0]); err != nil {
		t.Fatalf("timestamp %q not RFC3339: %v", fields[0], err)
	}
	if fields[1] != id || fields[2] != "1" {
		t.Fatalf("line = %q, want id %q choice 1", lines[0], id)
	}
}

func TestHomeReflectsVote(t *testing.T) {
	cases := []struct {
		answer string
		want   string
	}{
		{"yes", "You condemned Hasan Piker."},
		{"no", "You did not condemn Hasan Piker."},
	}
	for _, c := range cases {
		s, id := newVoteServer(t)
		postVote(t, s.routes(), id, c.answer)

		w := getHome(t, s.routes(), id)
		body := w.Body.String()
		if !strings.Contains(body, c.want) {
			t.Fatalf("after %q, page missing %q", c.answer, c.want)
		}
		if strings.Contains(body, `name="answer"`) {
			t.Fatalf("after %q, form still shown", c.answer)
		}
		if !strings.Contains(body, "1 person stands with you") {
			t.Fatalf("after %q, page missing tally", c.answer)
		}
	}
}

func TestTallySentence(t *testing.T) {
	cases := []struct {
		myChoice, withYou, others int
		want                      string
	}{
		{1, 3, 2, "3 people stand with you in condemning Hasan Piker, while 2 do not."},
		{0, 4, 1, "4 people stand with you in not condemning Hasan Piker, while 1 condemns him."},
		{1, 1, 1, "1 person stands with you in condemning Hasan Piker, while 1 does not."},
		{0, 2, 3, "2 people stand with you in not condemning Hasan Piker, while 3 condemn him."},
		{0, 1, 1, "1 person stands with you in not condemning Hasan Piker, while 1 condemns him."},
	}
	for _, c := range cases {
		if got := standWithSentence(c.myChoice, c.withYou, c.others); got != c.want {
			t.Fatalf("standWithSentence(%d,%d,%d) = %q, want %q",
				c.myChoice, c.withYou, c.others, got, c.want)
		}
	}
}

func TestTallyCountsReflectFile(t *testing.T) {
	s, id := newVoteServer(t)
	postVote(t, s.routes(), id, "yes")

	// two more yes votes and one no vote from other voters
	postVote(t, s.routes(), "voter-b", "yes")
	postVote(t, s.routes(), "voter-c", "yes")
	postVote(t, s.routes(), "voter-d", "no")

	body := getHome(t, s.routes(), id).Body.String()
	if want := "3 people stand with you in condemning Hasan Piker, while 1 does not."; !strings.Contains(body, want) {
		t.Fatalf("yes-voter tally missing %q in:\n%s", want, body)
	}

	body = getHome(t, s.routes(), "voter-d").Body.String()
	if want := "1 person stands with you in not condemning Hasan Piker, while 3 condemn him."; !strings.Contains(body, want) {
		t.Fatalf("no-voter tally missing %q in:\n%s", want, body)
	}
}

func TestCannotVoteTwiceOrChangeVote(t *testing.T) {
	s, id := newVoteServer(t)

	postVote(t, s.routes(), id, "yes")
	postVote(t, s.routes(), id, "no")

	lines := strings.Split(strings.TrimSpace(readResults(t, s)), "\n")
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "\t"+id+"\t1") {
		t.Fatalf("results = %q, want one immutable yes vote for %s", readResults(t, s), id)
	}

	w := getHome(t, s.routes(), id)
	if !strings.Contains(w.Body.String(), "You condemned Hasan Piker.") {
		t.Fatalf("page does not keep original vote")
	}
}

func TestInvalidAnswerRejected(t *testing.T) {
	s, id := newVoteServer(t)
	w := postVote(t, s.routes(), id, "maybe")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if got := readResults(t, s); got != "" {
		t.Fatalf("results = %q, want empty", got)
	}
}

func TestVoteWithoutCookieStillRecords(t *testing.T) {
	s, _ := newVoteServer(t)
	w := postVote(t, s.routes(), "", "yes")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == voterCookie && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q cookie to be set", voterCookie)
	}

	if got := readResults(t, s); !strings.HasSuffix(strings.TrimSpace(got), "\t1") {
		t.Fatalf("results = %q, want a single yes vote", got)
	}
}
