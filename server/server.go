// Package nntpserver provides everything you need for your own NNTP server.
package nntpserver

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"

	"github.com/dustin/go-nntp"
)

// An NNTPError is a coded NNTP error message.
type NNTPError struct {
	Code int
	Msg  string
}

// ErrServiceDiscontinued is returned for service discontinued.
var ErrServiceDiscontinued = &NNTPError{400, "Service discontinued"}

// ErrNoSuchGroup is returned for a request for a group that can't be found.
var ErrNoSuchGroup = &NNTPError{411, "No such newsgroup"}

// ErrNoSuchGroup is returned for a request that requires a current
// group when none has been selected.
var ErrNoGroupSelected = &NNTPError{412, "No newsgroup selected"}

// ErrNoCurrentArticle is returned when a command is executed that
// requires a current article when one has not been selected.
var ErrNoCurrentArticle = &NNTPError{420, "Current article number is invalid"}

// ErrInvalidArticleNumber is returned when an article is requested that can't be found.
var ErrInvalidArticleNumber = &NNTPError{423, "No article with that number"}

// ErrInvalidMessageID is returned when a message is requested that can't be found.
var ErrInvalidMessageID = &NNTPError{430, "No article with that message-id"}

// ErrNotWanted is returned when an attempt to post an article is
// rejected due the server not wanting the article.
var ErrNotWanted = &NNTPError{435, "Article not wanted"}

// ErrPostingNotPermitted is returned as the response to an attempt to
// post an article where posting is not permitted.
var ErrPostingNotPermitted = &NNTPError{440, "Posting not permitted"}

// ErrPostingFailed is returned when an attempt to post an article fails.
var ErrPostingFailed = &NNTPError{441, "posting failed"}

// ErrAuthRequired is returned to indicate authentication is required
// to proceed.
var ErrAuthRequired = &NNTPError{450, "authorization required"}

// ErrAuthRejected is returned for invalid authentication.
var ErrAuthRejected = &NNTPError{452, "authorization rejected"}

// ErrNotAuthenticated is returned when a command is issued that requires
// authentication, but authentication was not provided.
var ErrNotAuthenticated = &NNTPError{480, "authentication required"}

// ErrUnknownCommand is returned for unknown comands.
var ErrUnknownCommand = &NNTPError{500, "Unknown command"}

// ErrSyntax is returned when a command can't be parsed.
var ErrSyntax = &NNTPError{501, "not supported, or syntax error"}

// ErrPermissionDenied is returned when a command can't be executed due to lack
// of permissions.
var ErrPermissionDenied = &NNTPError{502, "access restriction, or permission denied"}

// ErrFault is returned when the command could not be handled because of a
// server error.
var ErrFault = &NNTPError{503, "program fault, command not performed"}

// Handler is a low-level protocol handler
type Handler func(args []string, s *session, c *textproto.Conn) error

// A NumberedArticle provides local sequence nubers to articles When
// listing articles in a group.
type NumberedArticle struct {
	Num     int64
	Article *nntp.Article
}

// The Backend that provides the things and does the stuff.
type Backend interface {
	ListGroups(max int) ([]*nntp.Group, error)
	GetGroup(name string) (*nntp.Group, error)
	GetArticleMsgId(group *nntp.Group, id string) (io.ReadCloser, int64, error)
	GetArticleNum(group *nntp.Group, num int64) (io.ReadCloser, string, error)
	GetArticles(group *nntp.Group, from, to int64) ([]NumberedArticle, error)
	Authorized() bool
	// Authenticate and optionally swap out the backend for this session.
	// You may return nil to continue using the same backend.
	Authenticate(user, pass string) (Backend, error)
	AllowPost() bool
	Post(article io.Reader) error
}

type session struct {
	server  *Server
	backend Backend
	group   *nntp.Group
}

// The Server handle.
type Server struct {
	// Handlers are dispatched by command name.
	Handlers map[string]Handler
	// The backend (your code) that provides data
	Backend Backend
	// The currently selected group.
	group *nntp.Group
}

// NewServer builds a new server handle request to a backend.
func NewServer(backend Backend) *Server {
	rv := Server{
		Handlers: make(map[string]Handler),
		Backend:  backend,
	}
	rv.Handlers[""] = handleDefault
	rv.Handlers["quit"] = handleQuit
	rv.Handlers["group"] = handleGroup
	rv.Handlers["list"] = handleList
	rv.Handlers["head"] = handleHead
	rv.Handlers["body"] = handleBody
	rv.Handlers["article"] = handleArticle
	rv.Handlers["post"] = handlePost
	rv.Handlers["ihave"] = handleIHave
	rv.Handlers["capabilities"] = handleCap
	rv.Handlers["mode"] = handleMode
	rv.Handlers["authinfo"] = handleAuthInfo
	rv.Handlers["newgroups"] = handleNewGroups
	rv.Handlers["over"] = handleOver
	rv.Handlers["xover"] = handleOver
	return &rv
}

func (e *NNTPError) Error() string {
	return fmt.Sprintf("%d %s", e.Code, e.Msg)
}

func (s *session) dispatchCommand(cmd string, args []string,
	c *textproto.Conn) (err error) {

	handler, found := s.server.Handlers[strings.ToLower(cmd)]
	if !found {
		handler, found = s.server.Handlers[""]
		if !found {
			panic("No default handler.")
		}
	}
	return handler(args, s, c)
}

// Process an NNTP session.
func (s *Server) Process(nc net.Conn) {
	defer nc.Close()
	c := textproto.NewConn(nc)

	sess := &session{
		server:  s,
		backend: s.Backend,
		group:   nil,
	}

	c.PrintfLine("200 Hello!")
	for {
		l, err := c.ReadLine()
		if err != nil {
			log.Printf("Error reading from client, dropping conn: %v", err)
			return
		}
		cmd := strings.Split(l, " ")
		log.Printf("Got cmd:  %+v", cmd)
		args := []string{}
		if len(cmd) > 1 {
			args = cmd[1:]
		}
		err = sess.dispatchCommand(cmd[0], args, c)
		if err != nil {
			_, isNNTPError := err.(*NNTPError)
			switch {
			case err == io.EOF:
				// Drop this connection silently. They hung up
				return
			case isNNTPError:
				c.PrintfLine(err.Error())
			default:
				log.Printf("Error dispatching command, dropping conn: %v",
					err)
				return
			}
		}
	}
}

func parseRange(spec string) (low, high int64) {
	if spec == "" {
		return 0, math.MaxInt64
	}
	parts := strings.Split(spec, "-")
	if len(parts) == 1 {
		h, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			h = math.MaxInt64
		}
		return 0, h
	}
	l, _ := strconv.ParseInt(parts[0], 10, 64)
	h, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		h = math.MaxInt64
	}
	return l, h
}

/*
   "0" or article number (see below)
   Subject header content
   From header content
   Date header content
   Message-ID header content
   References header content
   :bytes metadata item
   :lines metadata item
*/

func handleOver(args []string, s *session, c *textproto.Conn) error {
	if s.group == nil {
		return ErrNoGroupSelected
	}
	from, to := parseRange(args[0])
	articles, err := s.backend.GetArticles(s.group, from, to)
	if err != nil {
		return err
	}
	c.PrintfLine("224 here it comes")
	dw := c.DotWriter()
	defer dw.Close()
	for _, a := range articles {
		fmt.Fprintf(dw, "%d\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n", a.Num,
			a.Article.Header.Get("Subject"),
			a.Article.Header.Get("From"),
			a.Article.Header.Get("Date"),
			a.Article.Header.Get("Message-Id"),
			a.Article.Header.Get("References"),
			a.Article.Bytes, a.Article.Lines)
	}
	return nil
}

func handleListOverviewFmt(c *textproto.Conn) error {
	err := c.PrintfLine("215 Order of fields in overview database.")
	if err != nil {
		return err
	}
	dw := c.DotWriter()
	defer dw.Close()
	_, err = fmt.Fprintln(dw, `Subject:
From:
Date:
Message-ID:
References:
:bytes
:lines`)
	return err
}

func handleList(args []string, s *session, c *textproto.Conn) error {
	ltype := "active"
	if len(args) > 0 {
		ltype = strings.ToLower(args[0])
	}

	if ltype == "overview.fmt" {
		return handleListOverviewFmt(c)
	}

	groups, err := s.backend.ListGroups(-1)
	if err != nil {
		return err
	}
	c.PrintfLine("215 list of newsgroups follows")
	dw := c.DotWriter()
	defer dw.Close()
	for _, g := range groups {
		switch ltype {
		case "active":
			fmt.Fprintf(dw, "%s %d %d %v\r\n",
				g.Name, g.High, g.Low, g.Posting)
		case "newsgroups":
			fmt.Fprintf(dw, "%s %s\r\n", g.Name, g.Description)
		}
	}

	return nil
}

func handleNewGroups(args []string, s *session, c *textproto.Conn) error {
	c.PrintfLine("231 list of newsgroups follows")
	c.PrintfLine(".")
	return nil
}

func handleDefault(args []string, s *session, c *textproto.Conn) error {
	return ErrUnknownCommand
}

func handleQuit(args []string, s *session, c *textproto.Conn) error {
	c.PrintfLine("205 bye")
	return io.EOF
}

func handleGroup(args []string, s *session, c *textproto.Conn) error {
	if len(args) < 1 {
		return ErrNoSuchGroup
	}

	group, err := s.backend.GetGroup(args[0])
	if err != nil {
		return err
	}

	s.group = group

	c.PrintfLine("211 %d %d %d %s",
		group.Count, group.Low, group.High, group.Name)
	return nil
}

var MessageIdRegexp = regexp.MustCompile("^<(.*)>$")

func (s *session) getArticle(needGroup bool, args []string) (io.ReadCloser, int64, string, error) {
	if needGroup && s.group == nil {
		return nil, -1, "", ErrNoGroupSelected
	}
	if num, err := strconv.ParseInt(args[0], 10, 64); err == nil {
		art, msgId, err := s.backend.GetArticleNum(s.group, num)
		return art, num, msgId, err
	} else if m := MessageIdRegexp.FindStringSubmatch(args[0]); len(m) >= 2 {
		msgId := m[1]
		art, num, err := s.backend.GetArticleMsgId(s.group, msgId)
		return art, num, msgId, err
	} else {
		return nil, -1, "", ErrSyntax
	}
}

// readHeaders returns the headers inclusing \r\n
func readHeaders(artReader io.Reader) ([]string, io.Reader, error) {
	var reader = bufio.NewReader(artReader)
	var headers []string
	for {
		var line string
		for len(line) < 2 || line[len(line)-2] != '\r' {
			l, err := reader.ReadString(byte('\n'))
			if err != nil {
				return nil, reader, err
			}
			line += l
		}
		if line == "\r\n" {
			break
		}
		headers = append(headers, line)
	}
	return headers, reader, nil
}

/*
   Syntax
     HEAD message-id
     HEAD number
     HEAD


   First form (message-id specified)
     221 0|n message-id    Headers follow (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     221 n message-id      Headers follow (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     221 n message-id      Headers follow (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid
*/

func handleHead(args []string, s *session, c *textproto.Conn) error {
	article, num, msgId, err := s.getArticle(true, args)
	if err != nil {
		return err
	}
	defer article.Close()

	headers, _, err := readHeaders(article)
	if err != nil {
		return ErrFault
	}

	c.PrintfLine("221 %d %s", num, msgId)
	dw := c.DotWriter()
	defer dw.Close()
	for _, head := range headers {
		_, err = fmt.Fprint(dw, head)
		if err != nil {
			return err
		}
	}
	return nil
}

/*
   Syntax
     BODY message-id
     BODY number
     BODY

   Responses

   First form (message-id specified)
     222 0|n message-id    Body follows (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     222 n message-id      Body follows (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     222 n message-id      Body follows (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid

   Parameters
     number        Requested article number
     n             Returned article number
     message-id    Article message-id
*/

func handleBody(args []string, s *session, c *textproto.Conn) error {
	article, num, msgId, err := s.getArticle(true, args)
	if err != nil {
		return err
	}
	defer article.Close()

	_, body, err := readHeaders(article)

	c.PrintfLine("222 %d %s", num, msgId)
	dw := c.DotWriter()
	defer dw.Close()
	_, err = io.Copy(dw, body)
	return err
}

/*
   Syntax
     ARTICLE message-id
     ARTICLE number
     ARTICLE

   Responses

   First form (message-id specified)
     220 0|n message-id    Article follows (multi-line)
     430                   No article with that message-id

   Second form (article number specified)
     220 n message-id      Article follows (multi-line)
     412                   No newsgroup selected
     423                   No article with that number

   Third form (current article number used)
     220 n message-id      Article follows (multi-line)
     412                   No newsgroup selected
     420                   Current article number is invalid

   Parameters
     number        Requested article number
     n             Returned article number
     message-id    Article message-id
*/

func handleArticle(args []string, s *session, c *textproto.Conn) error {
	article, num, msgId, err := s.getArticle(true, args)
	if err != nil {
		return err
	}
	defer article.Close()

	art, err := ioutil.ReadAll(article)
	if err != nil {
		return ErrFault
	}

	c.PrintfLine("220 %d <%s>", num, msgId)
	dw := c.DotWriter()
	defer dw.Close()

	_, err = dw.Write(art)
	return err
}

/*
   Syntax
     POST

   Responses

   Initial responses
     340    Send article to be posted
     440    Posting not permitted

   Subsequent responses
     240    Article received OK
     441    Posting failed
*/

func handlePost(args []string, s *session, c *textproto.Conn) error {
	if !s.backend.AllowPost() {
		return ErrPostingNotPermitted
	}

	c.PrintfLine("340 Go ahead")
	var err error
	article := c.DotReader()
	err = s.backend.Post(article)
	if err != nil {
		return err
	}
	c.PrintfLine("240 article received OK")
	return nil
}

func handleIHave(args []string, s *session, c *textproto.Conn) error {
	if !s.backend.AllowPost() {
		return ErrNotWanted
	}

	// XXX:  See if we have it.
	article, _, _, err := s.getArticle(false, args)
	if article != nil {
		article.Close()
		return ErrNotWanted
	}

	c.PrintfLine("335 send it")
	postedArticle := c.DotReader()
	err = s.backend.Post(postedArticle)
	if err != nil {
		return err
	}
	c.PrintfLine("235 article received OK")
	return nil
}

func handleCap(args []string, s *session, c *textproto.Conn) error {
	c.PrintfLine("101 Capability list:")
	dw := c.DotWriter()
	defer dw.Close()

	fmt.Fprintf(dw, "VERSION 2\n")
	fmt.Fprintf(dw, "READER\n")
	if s.backend.AllowPost() {
		fmt.Fprintf(dw, "POST\n")
		fmt.Fprintf(dw, "IHAVE\n")
	}
	fmt.Fprintf(dw, "OVER\n")
	fmt.Fprintf(dw, "XOVER\n")
	fmt.Fprintf(dw, "LIST ACTIVE NEWSGROUPS OVERVIEW.FMT\n")
	return nil
}

func handleMode(args []string, s *session, c *textproto.Conn) error {
	if s.backend.AllowPost() {
		c.PrintfLine("200 Posting allowed")
	} else {
		c.PrintfLine("201 Posting prohibited")
	}
	return nil
}

func handleAuthInfo(args []string, s *session, c *textproto.Conn) error {
	if len(args) < 2 {
		return ErrSyntax
	}
	if strings.ToLower(args[0]) != "user" {
		return ErrSyntax
	}

	if s.backend.Authorized() {
		return c.PrintfLine("250 authenticated")
	}

	c.PrintfLine("350 Continue")
	a, err := c.ReadLine()
	parts := strings.SplitN(a, " ", 3)
	if strings.ToLower(parts[0]) != "authinfo" || strings.ToLower(parts[1]) != "pass" {
		return ErrSyntax
	}
	b, err := s.backend.Authenticate(args[1], parts[2])
	if err == nil {
		c.PrintfLine("250 authenticated")
		if b != nil {
			s.backend = b
		}
	}
	return err
}
