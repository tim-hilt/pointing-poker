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
	Users     map[string]*User
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

// TODO: Replace all log.Println with log.Printf

func (s *Session) handleMessageCurrentUser(data Data) {
	switch event := data.event; event {
	case USER_LEFT:
		log.Printf("User %s left session %s\n", data.MyUser.Name, s.Name)
	case USER_JOINED:
		log.Printf("User %s joined session %s\n", data.MyUser.Name, s.Name)
	case USER_VOTED:
		log.Printf("User %s voted %s in session %s\n", data.MyUser.Name, data.Vote, s.Name)

		var buf bytes.Buffer

		if s.allUsersVoted() {
			return
		}

		templates.ExecuteTemplate(&buf, "users", Data{
			MyUser:     data.MyUser,
			OtherUsers: s.getOtherUsers(data.MyUser.Name),
		})
		err := data.MyUser.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
		if err != nil {
			log.Println("Error writing message to user", data.MyUser.Name)
		}
	case RESET:
		// NOP
	case DEFAULT:
		// NOP
	default:
		// NOP
	}
}

// TODO: Check err for template.Execute.*-functions

func (s *Session) handleMessageOtherUsers(data Data) {
	var buf bytes.Buffer

	for userName, user := range s.Users {

		if userName == data.MyUser.Name {
			continue
		}

		switch event := data.event; event {
		case USER_LEFT:
			// NOP
		case USER_JOINED:
			templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     s.Users[userName],
				OtherUsers: s.getOtherUsers(userName),
			})
			err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				log.Println("Error writing message to user", userName)
			}
		case USER_VOTED:
			if s.allUsersVoted() {
				continue
			}

			templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     user,
				OtherUsers: s.getOtherUsers(userName),
			})
			err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				log.Println("Error writing message to user", userName)
			}
		case RESET:
			// NOP
		case DEFAULT:
			// NOP
		default:
			// NOP
		}
	}
}

func (s *Session) handleMessageAllUsers(data Data) {
	var buf bytes.Buffer

	if data.event == RESET {
		for _, user := range s.Users {
			user.Vote = -1
		}
	}

	if data.event == USER_LEFT && len(s.Users) == 0 {
		log.Printf("Last user left. Destroying session %s", s.Name)
		delete(sessions, s.Id)
		return
	}

	for userName, user := range s.Users {
		switch event := data.event; event {
		case USER_LEFT:
			templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     s.Users[userName],
				OtherUsers: s.getOtherUsers(userName),
			})
			err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				log.Println("Error writing message to user", userName)
			}
		case USER_JOINED:
			// NOP
		case USER_VOTED:
			if !s.allUsersVoted() {
				continue
			}

			votes := s.getVotes()
			average := util.Average(votes)
			median := util.Median(votes)
			recommendation := util.Recommendation(average, median, s.scale)

			log.Printf("All users voted in session %s. Average: %f, Median: %f, Recommendation: %d", s.Name, average, median, recommendation)

			templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:         user,
				OtherUsers:     s.getOtherUsers(userName),
				AllVoted:       true,
				Average:        average,
				Median:         median,
				Recommendation: recommendation,
			})
			err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				log.Println("Error writing message to user", userName)
			}
		case RESET:
			templates.ExecuteTemplate(&buf, "session", Data{
				MyUser:      user,
				OtherUsers:  s.getOtherUsers(userName),
				Scale:       s.scale,
				SessionId:   s.Id,
				SessionName: s.Name,
			})
			err := user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				log.Println("Error writing message to user", userName)
			}
		case DEFAULT:
			// NOP
		default:
			// NOP
		}
	}
}

func (s *Session) handleBroadcast() {
	for {
		data := <-s.broadcast

		go s.handleMessageCurrentUser(data)
		go s.handleMessageOtherUsers(data)
		go s.handleMessageAllUsers(data)
	}
}

func (s *Session) getOtherUsers(me string) []*User {
	users := make([]*User, 0, len(s.Users))
	for userName, user := range s.Users {
		if userName == me {
			continue
		}
		users = append(users, user)
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

// TODO: Evaluate whether this struct is really necessary & if all
// values are even used
type Data struct {
	event          Event
	Scale          util.Scale
	AllVoted       bool
	SessionName    string  `json:"name"`
	MyUser         *User   `json:"myUser"`
	OtherUsers     []*User `json:"users"`
	SessionId      string  `json:"id"`
	Vote           string  `json:"vote"`
	Average        float64
	Median         float64
	Recommendation int
}

var templates = template.Must(template.ParseGlob("templates/*.html"))
var templateIndex = template.Must(template.ParseFiles("templates/base.html", "templates/index.html"))
var templateJoinSession = template.Must(template.ParseFiles("templates/base.html", "templates/join-session.html"))
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
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Println("Could not accept connection", err)
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "Connection closed")

	user := &User{
		Name:       userName,
		Vote:       -1,
		Connection: c,
	}

	session := sessions[id]
	session.Lock()
	session.Users[userName] = user
	session.Unlock()
	defer delete(session.Users, userName)

	session.broadcast <- Data{
		event:     USER_JOINED,
		MyUser:    user,
		SessionId: id,
	}

	for {
		_, d, err := c.Read(r.Context())

		if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
			session.broadcast <- Data{event: USER_LEFT, MyUser: user}
			break
		}

		if err != nil {
			log.Println("Unknown error", err)
		}

		log.Println(userName, "received data", string(d))

		wsResponse := &HtmxWsResponse{}
		err = json.Unmarshal(d, wsResponse)
		if err != nil {
			log.Println("Error unmarshaling JSON:", err)
			continue
		}

		data := Data{MyUser: user}

		if wsResponse.Vote != "" {
			vote, err := strconv.Atoi(wsResponse.Vote)
			if err != nil {
				log.Printf("Couldn't parse vote '%s' to int\n", wsResponse.Vote)
			}

			session.Lock()
			session.Users[userName].Vote = vote
			session.Unlock()

			data.event = USER_VOTED
		} else if wsResponse.Headers.HxTrigger == "restart-session" {
			data.event = RESET
		}

		session.broadcast <- data
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
		Users:     make(map[string]*User),
		scale:     util.Scales[scale],
		broadcast: make(chan Data),
		Id:        sessionId,
		Name:      sessionName,
	}
	sessions[sessionId] = session

	go session.handleBroadcast()

	w.Header().Add("HX-Push-Url", "/"+sessionId)
	templates.ExecuteTemplate(w, "session", Data{
		Scale:       util.Scales[scale],
		MyUser:      &User{Name: userName, Vote: -1},
		SessionId:   sessionId,
		SessionName: sessions[sessionId].Name,
	})
}

func getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, ok := sessions[id]; !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("session with id " + id + " not found"))
		return
	}

	name := sessions[id].Name
	templateJoinSession.Execute(w, Data{SessionId: id, SessionName: name})
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
		Scale:       session.scale,
		OtherUsers:  session.getOtherUsers(userName),
		MyUser:      &User{Name: userName, Vote: -1},
		SessionId:   id,
		SessionName: session.Name,
	})
}

func index(w http.ResponseWriter, r *http.Request) {
	templateIndex.Execute(w, nil)
}

// getFavicon only exists, because browsers automatically
// request favicon.ico. If this route is not defined, it
// will match the route for /{id}, getSession
func getFavicon(w http.ResponseWriter, r *http.Request) {
	log.Println("favicon.ico requested")
}

func main() {
	http.HandleFunc("GET /", index)
	http.HandleFunc("GET /favicon.ico", getFavicon)
	http.HandleFunc("POST /create-session", newSession)
	http.HandleFunc("POST /join-session/{id}", joinSession)
	http.HandleFunc("GET /{id}", getSession)
	http.HandleFunc("GET /ws/{id}/{username}", handleWsConnection)

	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
