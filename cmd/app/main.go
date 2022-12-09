package main

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/mikejk8s/gmud/pkg/backend"
	mn "github.com/mikejk8s/gmud/pkg/menus"
	"github.com/mikejk8s/gmud/pkg/models"
	sqlpkg "github.com/mikejk8s/gmud/pkg/mysqlpkg"
	"github.com/mikejk8s/gmud/pkg/sshcommands"
	"github.com/muesli/termenv"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	lm "github.com/charmbracelet/wish/logging"
	"github.com/gliderlabs/ssh"
)

const (
	Host = "127.0.0.1"
	Port = 2222
)

var RunningOnDocker = false

func passHandler(ctx ssh.Context, password string) bool {
	// This means that the user is not signed up yet
	if ctx.User() == "" {
		return false // siktir git kayıt ol gel
	}
	usersConn := sqlpkg.SqlConn{}
	err := usersConn.GetSQLConn("users")
	if err != nil {
		log.Fatalln(err)
	}
	// data := usersDB.Exec(fmt.Sprintf("SELECT password from users.users where username = '%s'", ctx.User())).First(&p)
	user := models.User{}
	rows, _ := usersConn.DB.Query(fmt.Sprintf("SELECT password, email, name, username from users.users where username = '%s'", ctx.User()))
	for rows.Next() {
		err := rows.Scan(&user.Password, &user.Email, &user.Name, &user.Username)
		if err != nil {
			panic(err)
		}
	}
	usersConn.CloseConn()
	credentialError := user.CheckPassword(password)
	if credentialError != nil {
		return false
	} else {
		return true
	}
}
func main() {
	// This is used for switching between localhost:port to TCP_HOST:TCP_PORT etc.
	// Lookup for a running on docker environment variable, if exists
	tempEnvVar, ok := os.LookupEnv("RUNNING_ON_DOCKER")
	if ok {
		// set running on docker to true
		RunningOnDocker = tempEnvVar == "true"
		// Use mysqlpkg's RunningOnDocker variable that is set on vars.go if you want to use this bool in other code.
		sqlpkg.RunningOnDocker = RunningOnDocker
		log.Println("Running on docker is set to", sqlpkg.RunningOnDocker)
	} else {
		sqlpkg.RunningOnDocker = false
	}
	// I will try to set everything from one function so future contributors can change variables
	// from one place as they run the app on their own.
	//
	// Change the mysqlpkg variables if we are running on docker
	if RunningOnDocker {
		sqlpkg.Username = os.Getenv("MYSQL_USER")
		sqlpkg.Password = os.Getenv("MYSQL_PASSWORD")
		sqlpkg.Hostname = os.Getenv("MYSQL_HOST")
	} else {
		// If we are not running on docker, please change these variables as you desire.
		sqlpkg.Username = "cansu"
		sqlpkg.Password = "1234"
		sqlpkg.Hostname = "(127.0.0.1:3306)"
	}
	// Run a websocket server to communicate between players.
	// Fire the webpage server that will handle the signup page.
	//
	// This function will use WEBPAGE_HOST and WEBPAGE_ENV variables that is submitted on docker-compose.yml
	//
	// Or localhost:6969 if it's not running on docker. You can change it by changing 6969 below simply.
	go backend.StartWebPageBackend(RunningOnDocker, 6969)
	// Create users schema and users table, migrate if possible.
	go sqlpkg.Migration()
	// Connect to mariadb database and create characters schema + character tables if they don't exist
	initialTableCreation := sqlpkg.SqlConn{}
	err := initialTableCreation.GetSQLConn("characters")
	if err != nil {
		log.Println(err)
	}
	// Characters table creation.
	go func() {
		err := initialTableCreation.CreateCharacterTable()
		if err != nil {
			log.Println(err)
		} else {
			initialTableCreation.CloseConn()
		}
	}()
	// Users table creation.
	go func() {
		initialUsersCreation := sqlpkg.SqlConn{}
		err := initialUsersCreation.GetSQLConn("")
		if err != nil {
			log.Fatalln(err)
		}
		initialUsersCreation.CreateUsersTable()
		initialUsersCreation.CloseConn()
	}()
	// Initialize the SSH server
	s, err := wish.NewServer(
		wish.WithIdleTimeout(30*time.Minute), // 30-minute idle timer, in case if someone forgets to log out.
		wish.WithPasswordAuth(passHandler),
		wish.WithAddress(fmt.Sprintf("%s:%d", Host, Port)),
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			return true
		}),
		wish.WithMiddleware(
			lm.Middleware(),
			loginBubbleteaMiddleware(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}
	s.ConnectionFailedCallback = func(conn net.Conn, err error) {
		log.Println("Connection failed:", err)
	}
	done := make(chan os.Signal, 0)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("Starting SSH server on %s:%d", Host, Port)
	go func() {
		err = s.ListenAndServe()
		if err != nil {
			log.Fatalln(err)
		}
	}()
	<-done
	log.Println("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalln(err)
	}
}

func loginBubbleteaMiddleware() wish.Middleware {
	login := func(m tea.Model, opts ...tea.ProgramOption) *tea.Program {
		p := tea.NewProgram(m, opts...)
		go func() {
			// if err := p.Start(); err != nil {
			// 	log.Fatalln(err)
			// }
			for {
				<-time.After(1 * time.Second)
				p.Send(timeMsg(time.Now()))
			}
		}()
		return p
	}
	teaHandler := func(s ssh.Session) *tea.Program {
		pty, _, _ := s.Pty()
		m := model{
			SSHSession: s,
			Width:      pty.Window.Width,
			Height:     pty.Window.Height,
			time:       time.Now(),
			accOwner:   s.User(),
		}
		return login(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen(), tea.WithMouseCellMotion())
	}
	return bm.MiddlewareWithProgramHandler(teaHandler, termenv.ANSI256)
}

type model struct {
	SSHSession ssh.Session
	time       time.Time
	Height     int
	Width      int
	accOwner   string // Account owner will be used for matching the characters created from this account.
}

type timeMsg time.Time

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case timeMsg:
		m.time = time.Time(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "k":
			sshcommands.Test(m.SSHSession, "whois")
		case "q", "ctrl+c":
			return m, tea.Quit
		case "l", "ctrl+l":
			return mn.InitialModel(m.accOwner, m.SSHSession), nil // Go to the login page with passing account owner
		case "n", "ctrl+n":
			//mn.NewAccount()
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	s := "Welcome to gmud!\n\n"
	s += "Date> " + m.time.Format(time.RFC1123) + "\n\n"
	s += "Press 'l' to go in.\n"
	s += m.SSHSession.LocalAddr().String() + "\n"
	s += m.SSHSession.RemoteAddr().String() + "\n"
	return fmt.Sprintln(s, m.Height, m.Width)
}
