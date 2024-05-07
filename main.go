package main

import (
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

	"github.com/tim-hilt/pointing-poker-websockets/util"
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
	Scale          util.Scale
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

var templates = template.Must(template.ParseGlob("templates/*.html"))
var templateIndex = template.Must(template.ParseFiles("templates/base.html", "templates/index.html", "templates/blocks.html"))
var templateJoinSession = template.Must(template.ParseFiles("templates/base.html", "templates/join-session.html"))
var templateNotFound = template.Must(template.ParseFiles("templates/base.html", "templates/not-found.html", "templates/blocks.html"))
var templateSession = template.Must(template.ParseFiles("templates/base.html", "templates/session.html", "templates/blocks.html"))

var lockSessions sync.RWMutex
var sessions = make(map[string]*Session)

// TODO: Name collisions
// TODO: Test with Chrome

// TODO: Reloading an active session if user is only one left shouldn't go to the join-session dialog
// TODO: Log info about requester (ip, ...)
// TODO: Title not updated after creating session
// TODO: Current solution with fixed element for voting-candidates is not good
// TODO: Safari isn't saving cookies

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
	util.Logger.Info("incoming request", "route", route)

	cookieUserName, err := r.Cookie("username")

	user := &User{
		Name: "",
		Vote: -1,
	}

	if errors.Is(err, http.ErrNoCookie) {
		util.Logger.Info("new user wants to create session")
	} else if err != nil {
		util.Logger.Error("unexpected error while checking cookie", "error", err)
		return
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			util.Logger.Error("unexpected error while decoding cookie value")
		}
		util.Logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	err = templateIndex.Execute(w, Data{
		MyUser: user,
	})
	if err != nil {
		util.Logger.Error("could not execute template", "template", "index", "error", err)
	}
}

// getFavicon only exists, because browsers automatically
// request favicon.ico. If this route is not defined, it
// will match the route for /{id}, getSession
func getFavicon(w http.ResponseWriter, r *http.Request) {
	route := "GET /favicon.ico"
	util.Logger.Info("incoming request", "route", route)
}

func newSession(w http.ResponseWriter, r *http.Request) {
	route := "POST /create-session" // TODO: These vars could be generated automatically using r
	util.Logger.Info("incoming request", "route", route)

	err := r.ParseForm()
	if err != nil {
		util.Logger.Info("could not parse form", "route", route)
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
		util.Logger.Error("unexpected error while checking cookie", "error", err)
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			util.Logger.Error("unexpected error while decoding cookie value")
		}
		util.Logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	scale := form.Get("scale")
	sessionId := util.RandSeq(16)

	session := &Session{
		Users:     make(map[string]*User),
		scale:     util.Scales[scale],
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
		Scale:       util.Scales[scale],
		MyUser:      user,
		SessionId:   sessionId,
		SessionName: sessionName,
	})

	if err != nil {
		util.Logger.Error("could not execute template", "template", "session", "sessionName", sessionName, "error", err)
	}
}

func getSession(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")
	route := fmt.Sprintf("GET /%s", sessionId)
	util.Logger.Info("incoming request", "route", route)

	cookieUserName, err := r.Cookie("username")

	user := &User{
		Name: "",
		Vote: -1,
	}

	if errors.Is(err, http.ErrNoCookie) {
		util.Logger.Info("new user wants to join session")
	} else if err != nil {
		util.Logger.Error("unexpected error while checking cookie", "error", err)
	} else {
		un, err := base64.URLEncoding.DecodeString(cookieUserName.Value)
		if err != nil {
			util.Logger.Error("unexpected error while decoding cookie value")
		}
		util.Logger.Info("known user wants to create session", "userName", string(un))
		user.Name = string(un)
	}

	if _, ok := sessions[sessionId]; !ok {
		util.Logger.Warn("session does not exist", "sessionId", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
			MyUser:    user,
		})
		if err != nil {
			util.Logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "sessionId", sessionId)
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
			util.Logger.Error("could not execute template", "template", "session", "sessionName", session.Name, "error", err)
		}
		return
	}

	sessionName := sessions[sessionId].Name
	err = templateJoinSession.Execute(w, Data{
		SessionId:   sessionId,
		SessionName: sessionName,
	})

	if err != nil {
		util.Logger.Error("could not execute template", "template", "join-session", "sessionName", sessionName, "error", err)
	}
}

func joinSession(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")
	route := fmt.Sprintf("POST /join-session/%s", sessionId)
	util.Logger.Info("incoming request", "route", route)

	if _, ok := sessions[sessionId]; !ok {
		util.Logger.Warn("session does not exist", "sessionId", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
		})

		if err != nil {
			util.Logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "sessionId", sessionId)
		}

		return
	}

	err := r.ParseForm()
	if err != nil {
		util.Logger.Info("could not parse form", "route", "POST /create-session")
		return
	}

	form := r.Form
	userName := form.Get("username")
	userName = strings.TrimSpace(userName)

	cookieUserName := newUserNameCookie(userName)
	http.SetCookie(w, cookieUserName)

	session := sessions[sessionId]
	err = templates.ExecuteTemplate(w, "session", Data{
		Scale:       session.scale,
		OtherUsers:  session.getOtherUsers(userName),
		MyUser:      &User{Name: userName, Vote: -1},
		SessionId:   sessionId,
		SessionName: session.Name,
	})

	if err != nil {
		util.Logger.Error("could not execute template", "template", "session", "sessionName", session.Name, "error", err)
	}
}

func handleWsConnection(w http.ResponseWriter, r *http.Request) {
	sessionId := r.PathValue("id")

	cookieUserName, err := r.Cookie("username")

	if errors.Is(err, http.ErrNoCookie) {
		util.Logger.Error("username-cookie not set. Could not join session")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("username-cookie not set. Could not join session"))
		return
	} else if err != nil {
		util.Logger.Error("unexpected error while checking cookie", "error", err)
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
		util.Logger.Error("unexpected error while decoding cookie value")
	}
	util.Logger.Info("known user wants to create session", "userName", string(un))
	user.Name = string(un)

	route := fmt.Sprintf("GET /ws/%s", sessionId)
	util.Logger.Info("incoming request", "route", route)

	if _, ok := sessions[sessionId]; !ok {
		util.Logger.Warn("session does not exist", "sessionId", sessionId)
		w.WriteHeader(http.StatusNotFound)

		err := templateNotFound.Execute(w, Data{
			SessionId: sessionId,
		})
		if err != nil {
			util.Logger.Error("could not execute template", "template", "not-found", "route", route, "error", err, "sessionId", sessionId)
		}
		return
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		util.Logger.Error("session could not be joined", "user", user.Name, "sessionId", sessionId, "error", err)
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

		if err != nil {
			util.Logger.Error("unknown error", "error", err)
		}

		util.Logger.Info("message from websocket", "user", user.Name, "message", string(d))

		wsResponse := &HtmxWsResponse{}
		err = json.Unmarshal(d, wsResponse)
		if err != nil {
			util.Logger.Error("could not unmarshal json", "error", err)
			continue
		}

		data := Data{
			MyUser: user,
		}

		if wsResponse.Vote != "" {
			vote, err := strconv.Atoi(wsResponse.Vote)
			if err != nil {
				util.Logger.Error("couldn't parse vote to int", "vote", wsResponse.Vote, "error", err)
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

	err = c.Close(websocket.StatusNormalClosure, "Connection closed")

	if err != nil {
		util.Logger.Error("could not close websocket connection", "user", user.Name, "sessionId", sessionId)
	}
}

func main() {
	http.HandleFunc("GET /", index)
	http.HandleFunc("GET /favicon.ico", getFavicon)
	http.HandleFunc("POST /create-session", newSession)
	http.HandleFunc("GET /{id}", getSession)
	http.HandleFunc("POST /join-session/{id}", joinSession)
	http.HandleFunc("GET /ws/{id}", handleWsConnection)

	certDir := "/etc/letsencrypt/live/pointing-poker.duckdns.org"
	cert := path.Join(certDir, "fullchain.pem")
	key := path.Join(certDir, "privkey.pem")

	util.Logger.Info("starting server")

	if _, err := os.Stat(certDir); err == nil {
		// certificate found
		go http.ListenAndServeTLS("0.0.0.0:443", cert, key, nil)
		err := http.ListenAndServe("0.0.0.0:80", nil)
		if err != nil {
			util.Logger.Error("server exited unexpectedly", "error", err)
		}

	} else if errors.Is(err, os.ErrNotExist) {
		err := http.ListenAndServe(":8000", nil)
		if err != nil {
			util.Logger.Error("server exited unexpectedly", "error", err)
		}
	} else {
		util.Logger.Error("unexpected error", "error", err)

	}
}
