package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"nhooyr.io/websocket"
)

type Event int

const (
	DEFAULT Event = iota
	USER_LEFT
	USER_JOINED
	USER_VOTED
	RESET
)

type HtmxWsHeaders struct {
	HxRequest     string `json:"HX-Request"`
	HxTrigger     string `json:"HX-Trigger"`
	HxTriggerName string `json:"HX-Trigger-Name"`
	HxTarget      string `json:"HX-Target"`
	HxCurrentUrl  string `json:"HX-Current-URL"`
}

type HtmxWsResponse struct {
	Vote    string        `json:"vote"`
	Headers HtmxWsHeaders `json:"HEADERS"`
}

type Data struct {
	event          Event
	Scale          Scale
	AllVoted       bool
	SessionName    string
	MyUser         *User
	OtherUsers     []*User
	SessionId      string
	Vote           string
	Average        float64
	Median         float64
	Recommendation int
}

//go:embed web/template/*.html
var templatesFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "web/template/blocks.html"))
var templateIndex = template.Must(template.ParseFS(templatesFS, "web/template/base.html", "web/template/index.html", "web/template/blocks.html"))
var templateJoinSession = template.Must(template.ParseFS(templatesFS, "web/template/base.html", "web/template/join-session.html"))
var templateNotFound = template.Must(template.ParseFS(templatesFS, "web/template/base.html", "web/template/not-found.html", "web/template/blocks.html"))
var templateSession = template.Must(template.ParseFS(templatesFS, "web/template/base.html", "web/template/session.html", "web/template/blocks.html"))

var lockSessions sync.RWMutex
var sessions = make(map[string]*Session)

// TODO: Log info about requester (ip, ...)
// TODO: Instrumentation with Prometheus?
// TODO: Current solution with fixed element for voting-candidates is not good -> Maybe sticky footer?
// TODO: Write test cmd that spawns websocket clients
// TODO: username collisions
// TODO: Safari isn't saving cookies
// TODO: Styling: Dark Mode / Light Mode
// TODO: Styling: Responsive Design

func newUserNameCookie(userName string) *http.Cookie {
	return &http.Cookie{
		Name:     "username",
		Value:    base64.URLEncoding.EncodeToString([]byte(userName)),
		Path:     "/",
		MaxAge:   3600 * 24 * 365 * 5, // 5 years
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func index(w http.ResponseWriter, r *http.Request) {
	route := "/"
	logger.Info("incoming request", "route", route)

	cookieUserName, err := r.Cookie("username")

	user := &User{
		Name: "",
		Vote: -1,
	}

	if errors.Is(err, http.ErrNoCookie) {
		logger.Info("new user wants to create session")
	} else if err != nil {
		logger.Error("unexpected error while checking cookie", "error", err)
		return
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			logger.Error("unexpected error while decoding cookie value")
		}
		logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	err = templateIndex.Execute(w, Data{
		MyUser: user,
	})

	if err != nil {
		logger.Error("could not execute template", "template", "index", "error", err)
	}
}

// getFavicon only exists, because browsers automatically
// request favicon.ico. If this route is not defined, it
// will match the route for /{id}, getSession
func getFavicon(w http.ResponseWriter, r *http.Request) {
	route := "GET /favicon.ico"
	logger.Info("incoming request", "route", route)
}

func newSession(w http.ResponseWriter, r *http.Request) {
	route := "POST /create-session"
	logger.Info("incoming request", "route", route)

	err := r.ParseForm()
	if err != nil {
		logger.Info("could not parse form", "route", route)
		return
	}

	form := r.Form
	sessionName := form.Get("session-name")
	sessionName = strings.TrimSpace(sessionName)

	cookieUserName, err := r.Cookie("username")

	user := &User{
		Name: "",
		Vote: -1,
	}
	if errors.Is(err, http.ErrNoCookie) {
		userName := form.Get("username")
		userName = strings.TrimSpace(userName)
		user.Name = userName

		cookieUserName = newUserNameCookie(userName)
		http.SetCookie(w, cookieUserName)
	} else if err != nil {
		logger.Error("unexpected error while checking cookie", "error", err)
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			logger.Error("unexpected error while decoding cookie value")
		}
		logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	scale := form.Get("scale")
	sessionId := randSeq(16)

	session := &Session{
		Users:     make(map[string]*User),
		scale:     scales[scale],
		broadcast: make(chan Data),
		Id:        sessionId,
		Name:      sessionName,
	}

	lockSessions.Lock()
	sessions[sessionId] = session
	lockSessions.Unlock()

	go session.handleBroadcast()

	w.Header().Add("HX-Push-Url", "/"+sessionId)
	err = templates.ExecuteTemplate(w, "session", Data{
		Scale:       scales[scale],
		MyUser:      user,
		SessionId:   sessionId,
		SessionName: sessionName,
	})

	if err != nil {
		logger.Error("could not execute template", "template", "session", "session", sessionId, "error", err)
	}

	if _, err = w.Write([]byte("<title>Pointing Poker | " + session.Name + "</title>")); err != nil {
		logger.Error("could not write to response", "session", sessionId, "error", err)
	}

	if _, err = w.Write([]byte("<title>Pointing Poker | " + session.Name + "</title>")); err != nil {
		logger.Error("could not write to response", "session", sessionId, "error", err)
	}
}

func getSession(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")
	route := fmt.Sprintf("GET /%s", sessionId)
	logger.Info("incoming request", "route", route)

	cookieUserName, err := r.Cookie("username")

	user := &User{
		Name: "",
		Vote: -1,
	}

	if errors.Is(err, http.ErrNoCookie) {
		logger.Info("new user wants to join session")
	} else if err != nil {
		logger.Error("unexpected error while checking cookie", "error", err)
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			logger.Error("unexpected error while decoding cookie value")
		}
		logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	if _, ok := sessions[sessionId]; !ok {
		logger.Warn("session does not exist", "session", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
			MyUser:    user,
		})
		if err != nil {
			logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "session", sessionId)
		}
		return
	}

	if len(user.Name) > 0 {
		session := sessions[sessionId]
		err = templateSession.Execute(w, Data{
			MyUser:      user,
			OtherUsers:  session.getOtherUsers(user.Name),
			SessionId:   session.Id,
			SessionName: session.Name,
			Scale:       session.scale,
		})
		if err != nil {
			logger.Error("could not execute template", "template", "session", "session", sessionId, "error", err)
		}
		return
	}

	sessionName := sessions[sessionId].Name
	err = templateJoinSession.Execute(w, Data{
		SessionId:   sessionId,
		SessionName: sessionName,
	})

	if err != nil {
		logger.Error("could not execute template", "template", "join-session", "session", sessionId, "error", err)
	}
}

func joinSession(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")
	route := fmt.Sprintf("POST /join-session/%s", sessionId)
	logger.Info("incoming request", "route", route)

	if _, ok := sessions[sessionId]; !ok {
		logger.Warn("session does not exist", "sessionId", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
		})

		if err != nil {
			logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "session", sessionId)
		}

		return
	}

	if err := r.ParseForm(); err != nil {
		logger.Info("could not parse form", "route", "POST /create-session")
		return
	}

	form := r.Form
	userName := form.Get("username")
	userName = strings.TrimSpace(userName)

	cookieUserName := newUserNameCookie(userName)
	http.SetCookie(w, cookieUserName)

	session := sessions[sessionId]
	err := templates.ExecuteTemplate(w, "session", Data{
		Scale:       session.scale,
		OtherUsers:  session.getOtherUsers(userName),
		MyUser:      &User{Name: userName, Vote: -1},
		SessionId:   sessionId,
		SessionName: session.Name,
	})
	if err != nil {
		logger.Error("could not execute template", "template", "session", "session", sessionId, "error", err)
	}

	if _, err = w.Write([]byte("<title>Pointing Poker | " + session.Name + "</title>")); err != nil {
		logger.Error("could not write title", "session", sessionId, "error", err)
	}
}

func handleWsConnection(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")

	cookieUserName, err := r.Cookie("username")

	if errors.Is(err, http.ErrNoCookie) {
		logger.Error("username-cookie not set. Could not join session")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("username-cookie not set. Could not join session"))
		return
	} else if err != nil {
		logger.Error("unexpected error while checking cookie", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Something went wrong."))
		return
	}

	user := &User{
		Name: "",
		Vote: -1,
	}
	un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
	if err != nil {
		logger.Error("unexpected error while decoding cookie value")
	}
	logger.Info("known user wants to create session", "userName", string(un))
	user.Name = string(un)

	route := fmt.Sprintf("GET /ws/%s", sessionId)
	logger.Info("incoming request", "route", route)

	if _, ok := sessions[sessionId]; !ok {
		logger.Warn("session does not exist", "session", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
		})
		if err != nil {
			logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "session", sessionId)
		}
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Error("session could not be joined", "user", user.Name, "session", sessionId, "error", err)
		return
	}

	user.Connection = c

	session := sessions[sessionId]

	session.Lock()
	session.Users[user.Name] = user
	session.Unlock()

	defer delete(session.Users, user.Name)

	session.broadcast <- Data{
		event:     USER_JOINED,
		MyUser:    user,
		SessionId: sessionId,
	}

	for {
		_, d, err := c.Read(r.Context())

		if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
			session.broadcast <- Data{event: USER_LEFT, MyUser: user}
			break
		}

		if websocket.CloseStatus(err) == websocket.StatusNoStatusRcvd {
			logger.Info("websocket connection closed, possibly by timeout?", "session", sessionId, "user", user.Name)
			break
		}

		if err != nil {
			logger.Error("unknown error", "error", err)
		}

		logger.Info("message from websocket", "user", user.Name, "message", string(d))

		wsResponse := &HtmxWsResponse{}

		if err = json.Unmarshal(d, wsResponse); err != nil {
			logger.Error("could not unmarshal json", "error", err)
			continue
		}

		data := Data{
			MyUser: user,
		}

		if wsResponse.Vote != "" {
			vote, err := strconv.Atoi(wsResponse.Vote)
			if err != nil {
				logger.Error("couldn't parse vote to int", "vote", wsResponse.Vote, "error", err)
			}

			session.Lock()
			session.Users[user.Name].Vote = vote
			session.Unlock()

			data.event = USER_VOTED
		} else if wsResponse.Headers.HxTrigger == "restart-session" {
			data.event = RESET
		}

		session.broadcast <- data
	}

	if err = c.Close(websocket.StatusNormalClosure, "Connection closed"); err != nil {
		logger.Error("could not close websocket connection", "user", user.Name, "session", sessionId)
	}
}

//go:embed third_party/*
var scripts embed.FS

func main() {
	http.HandleFunc("GET /", index)
	http.HandleFunc("GET /favicon.ico", getFavicon)
	http.HandleFunc("POST /create-session", newSession)
	http.HandleFunc("GET /{id}", getSession)
	http.HandleFunc("POST /join-session/{id}", joinSession)
	http.HandleFunc("GET /ws/{id}", handleWsConnection)

	http.Handle("GET /scripts/", http.StripPrefix("/scripts/", http.FileServerFS(scripts)))

	certDir := "/etc/letsencrypt/live/tim-hilt.duckdns.org"
	cert := path.Join(certDir, "fullchain.pem")
	key := path.Join(certDir, "privkey.pem")

	logger.Info("starting server")

	if _, err := os.Stat(certDir); err == nil {
		// certificate found
		go http.ListenAndServeTLS("0.0.0.0:443", cert, key, nil)

		if err = http.ListenAndServe("0.0.0.0:80", nil); err != nil {
			logger.Error("server exited unexpectedly", "error", err)
		}

	} else if errors.Is(err, os.ErrNotExist) {
		if err = http.ListenAndServe(":8000", nil); err != nil {
			logger.Error("server exited unexpectedly", "error", err)
		}
	} else {
		logger.Error("unexpected error", "error", err)

	}
}
