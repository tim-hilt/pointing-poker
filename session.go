package main

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"nhooyr.io/websocket"
)

type User struct {
	Name       string
	Vote       int
	Connection *websocket.Conn
}

type Session struct {
	Users     map[string]*User
	scale     Scale
	broadcast chan Data
	Id        string
	Name      string
	sync.RWMutex
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

var activeSessions = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "sessions_active",
	Help: "How many sessions are currently active",
})

var activeUsers = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "users_active",
	Help: "How many users are currently active",
})

var totalEstimations = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "estimations_total",
	Help: "How many estimations have been processed",
})

func (s *Session) handleBroadcast() {
	for {
		select {
		case msg := <-s.broadcast:
			s.handleEvent(msg)
		case <-time.After(1 * time.Hour):
			s.handleTimeout()
			return
		}
	}
}

func (s *Session) handleEvent(msg Data) {
	switch msg.event {
	case USER_JOINED:
		s.handleUserJoined(msg)
	case USER_LEFT:
		s.handleUserLeft(msg)
	case USER_VOTED:
		s.handleUserVoted(msg)
	case RESET:
		s.handleReset(msg)
	case DEFAULT:
		fallthrough
	default:
		// logger.Error("should never reach here")
	}
}

func (s *Session) handleTimeout() {
	// logger.Info("deleting session after one hour of inactivity", "session", s.Id)
	s.executeAllUsers(func(user *User) {
		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "timeout", Data{
			SessionName: s.Name,
		})

		if err != nil {
			// logger.Error("could not execute template", "template", "timeout", "session", s.Id, "user", user.Name, "error", err)
		}

		if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
		}
	})
	delete(sessions, s.Id)
	activeSessions.Dec()
}

func (s *Session) handleUserJoined(msg Data) {
	// logger.Info("user joined session", "publisher", msg.MyUser.Name, "session", s.Id)
	activeUsers.Inc()

	go s.executeSubscribers(msg.MyUser.Name, func(user *User) {
		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "users", Data{
			MyUser:     user,
			OtherUsers: s.getOtherUsers(user.Name),
		})

		if err != nil {
			// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", user.Name, "error", err)
		}

		if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
		}
	})
}

func (s *Session) handleUserLeft(msg Data) {
	// logger.Info("user left session", "user", msg.MyUser.Name, "session", s.Id)
	activeUsers.Dec()

	d := Data{}
	if s.allUsersVoted() {
		votes := s.getVotes()
		average := average(votes)
		median := median(votes)
		recommendation := recommendation(average, median, s.scale)

		d.AllVoted = true
		d.Average = average
		d.Median = median
		d.Recommendation = recommendation
	}

	go s.executeAllUsers(func(user *User) {
		data := d
		data.MyUser = user
		data.OtherUsers = s.getOtherUsers(user.Name)

		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "users", data)
		if err != nil {
			// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", user.Name, "error", err)
		}

		if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
		}
	})
}

func (s *Session) handleUserVoted(msg Data) {
	// logger.Info("new vote", "user", msg.MyUser.Name, "session", s.Id, "vote", msg.Vote)

	if s.allUsersVoted() {
		totalEstimations.Inc()

		votes := s.getVotes()
		average := average(votes)
		median := median(votes)
		recommendation := recommendation(average, median, s.scale)

		d := Data{
			AllVoted:       true,
			Average:        average,
			Median:         median,
			Recommendation: recommendation,
		}
		// logger.Info("all users voted", "session", s.Id, "average", average, "median", median, "recommendation", recommendation)

		go s.executeAllUsers(func(user *User) {
			var buf bytes.Buffer

			data := d
			data.MyUser = user
			data.OtherUsers = s.getOtherUsers(user.Name)

			err := templates.ExecuteTemplate(&buf, "users", data)
			if err != nil {
				// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", user.Name, "error", err)
			}

			if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
				// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
			}
		})
		return
	}

	go func() {
		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "users", Data{
			// TODO: Do we have a nil pointer here?
			MyUser:     msg.MyUser,
			OtherUsers: s.getOtherUsers(msg.MyUser.Name),
		})
		if err != nil {
			// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", msg.MyUser.Name, "error", err)
		}

		if err = msg.MyUser.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", msg.MyUser.Name, "error", err)
		}
	}()

	go s.executeSubscribers(msg.MyUser.Name, func(user *User) {
		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "users", Data{
			MyUser:     user,
			OtherUsers: s.getOtherUsers(user.Name),
		})
		if err != nil {
			// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", user.Name, "error", err)
		}

		if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
		}
	})
}

func (s *Session) handleReset(msg Data) {
	// logger.Info("restarting session", "session", s.Id, "user", msg.MyUser.Name)
	for _, user := range s.Users {
		user.Vote = -1
	}

	go s.executeAllUsers(func(user *User) {
		var buf bytes.Buffer
		err := templates.ExecuteTemplate(&buf, "session-content", Data{
			MyUser:      user,
			OtherUsers:  s.getOtherUsers(user.Name),
			Scale:       s.scale,
			SessionId:   s.Id,
			SessionName: s.Name,
		})
		if err != nil {
			// logger.Error("could not execute template", "template", "users", "session", s.Id, "user", user.Name, "error", err)
		}

		if err = user.Connection.Write(context.Background(), websocket.MessageText, buf.Bytes()); err != nil {
			// logger.Error("could not write message to user", "message", buf.String(), "session", s.Id, "user", user.Name, "error", err)
		}
	})
}

func (s *Session) executeAllUsers(action func(user *User)) {
	for _, user := range s.Users {
		go action(user)
	}
}

func (s *Session) executeSubscribers(publisher string, action func(user *User)) {
	for _, user := range s.getOtherUsers(publisher) {
		go action(user)
	}
}
