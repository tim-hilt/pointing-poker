package main

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/tim-hilt/go-stdlib-htmx/util"
	"nhooyr.io/websocket"
)

type User struct {
	Name       string
	Vote       int
	Connection *websocket.Conn
}

type Session struct {
	Users     map[string]User
	scale     util.Scale
	broadcast chan Data
	Id        string
	Name      string
	sync.RWMutex
}

func (s *Session) allUsersVoted() bool {
	for _, user := range s.Users {
		if user.Vote < 0 {
			return false
		}
	}
	return true
}

func (s *Session) getVotes() []int {
	votes := make([]int, 0, len(s.Users))
	for _, user := range s.Users {
		votes = append(votes, user.Vote)
	}
	return votes
}

func (s *Session) handleBroadcast() {
	for {
		data := <-s.broadcast
		event := data.event

		if event == USER_LEFT && len(s.Users) == 0 {
			delete(sessions, s.Id)
			return
		}

		for userName, user := range s.Users {
			var buf bytes.Buffer

			switch event {
			case USER_LEFT:
				fallthrough
			case USER_JOINED:
				if userName == data.UserName {
					continue
				}
				templates.ExecuteTemplate(&buf, "users", Data{
					Users: s.getUsers(),
				})
				err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
				if err != nil {
					log.Println("Error broadcasting message:", err)
				}
			case USER_VOTED:
				log.Println(data.UserName, "voted:", data.Vote)

				vote, err := strconv.Atoi(data.Vote)
				if err != nil {
					log.Println("Couldn't parse", data.Vote, "to int")
				}

				s.Lock()
				user.Vote = vote
				s.Unlock()

				// if s.allUsersVoted() {
				// 	// TODO: Generate vote-result and render html
				// 	votes := s.getVotes()
				// 	av := average(votes)
				// 	med := median(votes)
				// 	recommendation := recommendation(av, med, s.scale)
				// }
			case RESET:
				// TODO:
				fallthrough
			case DEFAULT:
				// TODO:
				fallthrough
			default:
				// Should never get here
				continue
			}

		}
	}
}

func (s *Session) getUsers() []string {
	users := make([]string, 0, len(s.Users))
	for _, user := range s.Users {
		users = append(users, user.Name)
	}
	return users
}

type Event int

const (
	DEFAULT Event = iota
	USER_LEFT
	USER_JOINED
	USER_VOTED
	RESET
)

type Data struct {
	event    Event
	Scale    util.Scale
	Name     string   `json:"name"`
	UserName string   `json:"userName"`
	Users    []string `json:"users"`
	Id       string   `json:"id"`
	Vote     string   `json:"vote"`
}

var templates = template.Must(template.ParseGlob("templates/*.html"))
var sessions = make(map[string]*Session)

func handleWsConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, ok := sessions[id]; !ok {
		log.Println("Session", id, "not found")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("session with id " + id + " not found"))
		return
	}

	userName := r.PathValue("username")
	log.Println(userName, "joined session", id)
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Println("Could not accept connection", err)
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "Connection closed")

	session := sessions[id]
	session.Lock()
	session.Users[userName] = User{
		Name:       userName,
		Vote:       -1,
		Connection: c,
	}
	session.Unlock()
	defer delete(session.Users, userName)

	session.broadcast <- Data{
		event:    USER_JOINED,
		UserName: userName,
	}

	for {
		_, d, err := c.Read(r.Context())
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
			log.Println("Connection to", userName, "closed by client")
			session.broadcast <- Data{event: USER_LEFT, UserName: userName}
			break
		}

		if err != nil {
			log.Println("Unknown error", err)
		}

		log.Println(userName, "received data", string(d))

		data := &Data{event: USER_VOTED, UserName: userName}
		err = json.Unmarshal(d, data)
		if err != nil {
			log.Println("Error unmarshaling JSON:", err)
			continue
		}

		log.Println(userName, "sent message:", data)

		session.broadcast <- *data
	}
}

func newSession(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		log.Println("Could not parse form")
		return
	}

	form := r.Form
	sessionName := form.Get("session-name")
	userName := form.Get("username")
	scale := form.Get("scale")
	sessionId := util.RandSeq(16)

	session := &Session{
		Users:     make(map[string]User),
		scale:     util.Scales[scale],
		broadcast: make(chan Data),
		Id:        sessionId,
		Name:      sessionName,
	}
	sessions[sessionId] = session

	go session.handleBroadcast() // TODO: close broadcast channel by closing the context

	w.Header().Add("HX-Push-Url", "/"+sessionId)
	templates.ExecuteTemplate(w, "session", Data{
		Scale:    util.Scales[scale],
		UserName: userName,
		Id:       sessionId,
		Name:     sessions[sessionId].Name,
	})
}

func getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, ok := sessions[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("session with id " + id + " not found"))
		return
	}

	Name := sessions[id].Name
	templates.ExecuteTemplate(w, "join-session", Data{Id: id, Name: Name})
}

func joinSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, ok := sessions[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("session with id " + id + " not found"))
		return
	}

	err := r.ParseForm()
	if err != nil {
		log.Println("Could not parse form")
		return
	}

	form := r.Form
	userName := form.Get("username")
	session := sessions[id]
	templates.ExecuteTemplate(w, "session", Data{
		Scale:    session.scale,
		Users:    session.getUsers(),
		UserName: userName,
		Id:       id,
		Name:     session.Name,
	})
}

func index(w http.ResponseWriter, r *http.Request) {
	templates.ExecuteTemplate(w, "index", nil)
}

func getFavicon(w http.ResponseWriter, r *http.Request) {
	log.Println("favicon.ico requested")
}

func main() {
	http.HandleFunc("GET /", index)
	http.HandleFunc("GET /favicon.ico", getFavicon)
	http.HandleFunc("GET /{id}", getSession)
	http.HandleFunc("POST /create-session", newSession)
	http.HandleFunc("POST /join-session/{id}", joinSession)
	http.HandleFunc("GET /ws/{id}/{username}", handleWsConnection)

	log.Fatal(http.ListenAndServe(":8000", nil))
}
