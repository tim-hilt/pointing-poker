package main

import (
	"bytes"
	"context"
	"sync"

	"github.com/tim-hilt/pointing-poker-websockets/util"
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

func (s *Session) handleMessageCurrentUser(data Data) {
	switch event := data.event; event {
	case USER_LEFT:
		util.Logger.Info("user left session", "user", data.MyUser.Name, "sessionName", s.Name)
	case USER_JOINED:
		util.Logger.Info("user joined session", "user", data.MyUser.Name, "sessionName", s.Name)
	case USER_VOTED:
		util.Logger.Info("user voted", "user", data.MyUser.Name, "sessionName", s.Name, "vote", data.Vote)

		var buf bytes.Buffer

		if s.allUsersVoted() {
			return
		}

		err := templates.ExecuteTemplate(&buf, "users", Data{
			MyUser:     data.MyUser,
			OtherUsers: s.getOtherUsers(data.MyUser.Name),
		})
		if err != nil {
			util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
		}
		err = data.MyUser.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
		if err != nil {
			util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
		}
	case RESET:
		// NOP
	case DEFAULT:
		// NOP
	default:
		// NOP
	}
}

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
			err := templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     s.Users[userName],
				OtherUsers: s.getOtherUsers(userName),
			})

			if err != nil {
				util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}

			err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}
		case USER_VOTED:
			if s.allUsersVoted() {
				continue
			}

			err := templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     user,
				OtherUsers: s.getOtherUsers(userName),
			})
			if err != nil {
				util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}

			err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
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
		util.Logger.Info("restarting session", "sessionName", s.Name, "user", data.MyUser.Name)
		for _, user := range s.Users {
			user.Vote = -1
		}
	}

	if data.event == USER_LEFT && len(s.Users) == 0 {
		util.Logger.Info("last user left. Destroying session", "sessionName", s.Name, "lastUser", data.MyUser.Name)
		delete(sessions, s.Id)
		return
	}

	for userName, user := range s.Users {
		switch event := data.event; event {
		case USER_LEFT:
			err := templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:     s.Users[userName],
				OtherUsers: s.getOtherUsers(userName),
			})
			if err != nil {
				util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}

			err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
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

			util.Logger.Info("all users voted", "sessionName", s.Name, "average", average, "median", median, "recommendation", recommendation)

			err := templates.ExecuteTemplate(&buf, "users", Data{
				MyUser:         user,
				OtherUsers:     s.getOtherUsers(userName),
				AllVoted:       true,
				Average:        average,
				Median:         median,
				Recommendation: recommendation,
			})
			if err != nil {
				util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}

			err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}
		case RESET:
			err := templates.ExecuteTemplate(&buf, "session-content", Data{
				MyUser:      user,
				OtherUsers:  s.getOtherUsers(userName),
				Scale:       s.scale,
				SessionId:   s.Id,
				SessionName: s.Name,
			})
			if err != nil {
				util.Logger.Error("could not execute template", "template", "users", "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
			}

			err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes())
			if err != nil {
				util.Logger.Error("could not write message to user", "message", buf.String(), "sessionName", s.Name, "user", data.MyUser.Name, "error", err)
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
