package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type server struct {
	startedAt time.Time
	mu        sync.Mutex
	votesFile string
}

type ctxKey int

const voterIDKey ctxKey = iota

const voterCookie = "voter_id"

func main() {
	s := &server{startedAt: time.Now()}
	if v := os.Getenv("RESULTS_PATH"); v != "" {
		s.votesFile = v
	}

	r := s.routes()

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}

	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}

func now() time.Time {
	return time.Now()
}

func (s *server) routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.ensureVoterID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/", s.handleHome)
	r.Post("/vote", s.handleVote)

	return r
}

// ensureVoterID gives every visitor a stable cookie-backed UUID, available
// to handlers via voterIDFrom(r).
func (s *server) ensureVoterID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if c, err := r.Cookie(voterCookie); err == nil {
			id = c.Value
		}
		if id == "" {
			id = uuid.NewString()
			http.SetCookie(w, &http.Cookie{
				Name:     voterCookie,
				Value:    id,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   365 * 24 * 60 * 60,
			})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), voterIDKey, id)))
	})
}

func voterIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(voterIDKey).(string)
	return id
}

func (s *server) votesPath() string {
	if s.votesFile != "" {
		return s.votesFile
	}
	return "results.txt"
}

// voteFor reports the recorded choice (1 = yes, 0 = no) for id, if any.
func (s *server) voteFor(id string) (int, bool) {
	if id == "" {
		return 0, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.votesPath())
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 3 && fields[1] == id {
			choice, err := strconv.Atoi(fields[2])
			if err != nil {
				return 0, false
			}
			return choice, true
		}
	}
	return 0, false
}

// castVote appends id's vote unless id already has one. It reports whether a
// new vote was recorded so callers can tell first votes from repeat attempts.
func (s *server) castVote(id string, vote int) (recorded bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if b, rerr := os.ReadFile(s.votesPath()); rerr == nil {
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) == 3 && fields[1] == id {
				return false, nil
			}
		}
	}

	f, err := os.OpenFile(s.votesPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(f, "%s\t%s\t%d\n", ts, id, vote); err != nil {
		return false, err
	}
	return true, nil
}

const pageStyle = `<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    display: flex;
    flex-direction: column;
    justify-content: center;
    align-items: center;
    gap: 2.5rem;
    font-family: Georgia, "Times New Roman", serif;
    background: #0f0f13;
    color: #f4f4f5;
    text-align: center;
    padding: 1rem;
  }
  h1 {
    font-size: clamp(1.75rem, 5vw, 4rem);
    font-weight: 400;
    max-width: 28ch;
    line-height: 1.2;
  }
  .buttons { display: flex; gap: 1.5rem; flex-wrap: wrap; justify-content: center; }
  button {
    font-family: inherit;
    font-size: clamp(1.25rem, 2.5vw, 1.75rem);
    padding: 0.75rem 3.5rem;
    border-radius: 999px;
    cursor: pointer;
    border: 2px solid;
    transition: transform 0.15s ease, filter 0.15s ease;
  }
  button:hover { transform: scale(1.05); filter: brightness(1.1); }
  .yes { background: #15803d; color: #fff; border-color: #22c55e; }
  .no  { background: #b91c1c; color: #fff; border-color: #ef4444; }
  .note { color: #a1a1aa; font-family: system-ui, sans-serif; font-size: 1.1rem; }
  footer {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0.75rem;
    font-family: system-ui, sans-serif;
    font-size: 0.8rem;
  }
  footer { color: #666; }
  footer a { color: #71717a; text-decoration: none; }
  footer a:hover { color: #d4d4d8; text-decoration: underline; }
</style>`

// voteCounts returns how many recorded votes chose yes (1) and no (0).
func (s *server) voteCounts() (yes, no int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.votesPath())
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		switch fields[2] {
		case "1":
			yes++
		case "0":
			no++
		}
	}
	return yes, no
}

// standWithSentence words the totals for a voter based on their own choice.
// withYou is the count sharing the voter's stance, others the opposite count.
func standWithSentence(myChoice, withYou, others int) string {
	action := "condemning"
	if myChoice == 0 {
		action = "not condemning"
	}

	noun := "people"
	if withYou == 1 {
		noun = "person"
	}
	verb := "stand"
	if withYou == 1 {
		verb = "stands"
	}

	var rest string
	if myChoice == 1 {
		aux := "do"
		if others == 1 {
			aux = "does"
		}
		rest = fmt.Sprintf("while %d %s not.", others, aux)
	} else {
		otherVerb := "condemn"
		if others == 1 {
			otherVerb = "condemns"
		}
		rest = fmt.Sprintf("while %d %s him.", others, otherVerb)
	}

	return fmt.Sprintf("%d %s %s with you in %s Hasan, %s",
		withYou, noun, verb, action, rest)
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	id := voterIDFrom(r)

	var heading string
	var tally string
	showForm := true

	if choice, ok := s.voteFor(id); ok {
		showForm = false
		if choice == 1 {
			heading = "You condemned Hasan Piker."
		} else {
			heading = "You did not condemn Hasan Piker."
		}
		yes, no := s.voteCounts()
		if choice == 1 {
			tally = standWithSentence(choice, yes, no)
		} else {
			tally = standWithSentence(choice, no, yes)
		}
	} else {
		heading = "Do you condemn Hasan Piker?"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Do you condemn Hasan Piker?</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20viewBox='0%200%2064%2064'%3E%3Crect%20width='64'%20height='64'%20rx='14'%20fill='%230f0f13'/%3E%3Ctext%20x='32'%20y='46'%20font-family='Georgia,serif'%20font-size='40'%20fill='%23f4f4f5'%20text-anchor='middle'%3EH%3C/text%3E%3C/svg%3E">
` + pageStyle + `
</head>
<body>
  <h1>` + heading + `</h1>`))

	if showForm {
		w.Write([]byte(`
  <form method="post" action="/vote">
    <div class="buttons">
      <button class="yes" type="submit" name="answer" value="yes">Yes</button>
      <button class="no" type="submit" name="answer" value="no">No</button>
    </div>
  </form>`))
	} else {
		w.Write([]byte(`
  <p class="note">` + tally + `</p>
  <footer>
<a href="https://github.com/rcy/condemnhasan">github</a> |
<a href="https://www.reddit.com/r/Hasan_Piker/comments/1w80fhe/do_you_condemn_hasan_piker/">reddit</a>
</footer>`))
	}

	w.Write([]byte(`
</body>
</html>
`))
}

func (s *server) handleVote(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not parse form", http.StatusBadRequest)
		return
	}

	id := voterIDFrom(r)
	if id == "" {
		http.Error(w, "no voter id", http.StatusBadRequest)
		return
	}

	vote := 0
	switch r.FormValue("answer") {
	case "yes":
		vote = 1
	case "no":
		// already 0
	default:
		http.Error(w, `answer must be "yes" or "no"`, http.StatusBadRequest)
		return
	}

	recorded, err := s.castVote(id, vote)
	if err != nil {
		log.Printf("record vote: %v", err)
		http.Error(w, "could not record vote", http.StatusInternalServerError)
		return
	}
	if !recorded {
		log.Printf("voter %s tried to vote again; ignoring", id)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
