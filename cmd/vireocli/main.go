// Command vireocli is a command-line peer for vireo daemons.
//
//	vireocli                 # identity is generated once and reused
//	vireocli localhost       # connect on start
//
// The client id is the identity the daemon scopes sessions by. It is generated
// on first run, stored under the user config directory, and reused afterwards,
// so conversations survive between runs. Delete that file to become someone
// else.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"vireo/internal/client"
	"vireo/internal/protocol"
)

// choice is one numbered item on the current screen.
type choice struct {
	kind    string // "agent" or "session"
	agent   string
	session string
}

type conn struct {
	*client.Client
	addr     string
	agent    string            // open agent; "" = endpoint level
	sessions map[string]string // agent → current session id
	choices  []choice
}

func main() {
	proto := flag.String("proto", "tls", "transport: tls or quic")
	insecure := flag.Bool("insecure", true, "skip certificate verification (dev only)")
	idFlag := flag.String("id", "", "client id override (default: generated once and stored in the config dir)")
	name := flag.String("name", "vireocli", "human-readable client name")
	timeout := flag.Duration("timeout", 15*time.Second, "per-request timeout")
	flag.Parse()

	host := ""
	if args := flag.Args(); len(args) > 0 {
		host = strings.TrimSpace(args[0])
	}

	clientID := strings.TrimSpace(*idFlag)
	idSource := "flag -id"
	if clientID == "" {
		generated, path, err := loadOrCreateClientID()
		if err != nil {
			fmt.Printf("client id: %v\n", err)
			return
		}
		clientID, idSource = generated, path
	}
	if err := checkClientID(clientID); err != nil {
		fmt.Printf("client id %q: %v\n", clientID, err)
		return
	}
	fmt.Printf("client id: %s (%s)\n", clientID, idSource)

	opts := client.Options{
		ClientID: clientID,
		Name:     *name,
		Timeout:  *timeout,
		Insecure: *insecure,
		UseQUIC:  *proto == "quic",
	}

	c := &conn{sessions: map[string]string{}}
	if host != "" {
		nc, err := doConnect(opts, host)
		if err != nil {
			fmt.Printf("connect failed: %v\n", err)
		} else {
			c = nc
		}
	}
	if c.Client == nil {
		fmt.Printf("usage: vireocli <host>   — or type: connect <host>   (e.g. connect localhost)\n")
	}

	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(prompt(c))
		if !in.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		cmd := strings.ToLower(fields[0])

		switch cmd {
		case "quit", "exit", "q":
			return
		case "help", "h", "?":
			printHelp()
			continue
		case "connect":
			if len(fields) < 2 {
				fmt.Println("usage: connect <host or host:port>")
				continue
			}
			nc, err := doConnect(opts, fields[1])
			if err != nil {
				fmt.Printf("connect failed: %v\n", err)
				continue
			}
			c = nc
			continue
		}

		if c.Client == nil {
			fmt.Println("not connected — type: connect <host>")
			continue
		}

		if n, err := strconv.Atoi(cmd); err == nil {
			selectNumber(c, n)
			continue
		}

		switch cmd {
		case "agents", "menu":
			c.agent = ""
			showEndpoint(c)
			continue
		case "back":
			c.agent = ""
			showEndpoint(c)
			continue
		case "sessions", "chats", "list", "conversations":
			if c.agent == "" {
				c.agent = ""
				showEndpoint(c)
				continue
			}
			showAgent(c)
			continue
		case "new":
			if c.agent == "" {
				fmt.Println("open an agent first — or type: new <agent>")
				continue
			}
			newSession(c, c.agent)
			continue
		case "resume":
			if c.agent == "" {
				fmt.Println("open an agent first")
				continue
			}
			resumeSession(c)
			continue
		case "latest":
			if c.agent == "" {
				fmt.Println("open an agent first")
				continue
			}
			latestSession(c)
			continue
		case "close":
			if c.agent == "" {
				fmt.Println("open an agent first")
				continue
			}
			closeSession(c)
			continue
		}

		if _, ok := c.Agent(cmd); ok {
			openAgent(c, cmd)
			continue
		}

		if c.agent == "" {
			fmt.Println("type a number, an agent name, or: help")
			continue
		}
		sendMessage(c, line)
	}
}

// --- identity ---------------------------------------------------------------

// clientIDPath is where the generated identity lives, so conversations can be
// resumed between runs. Delete the file to start as somebody else.
func clientIDPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return ".vireo-client-id"
		}
		dir = home
	}
	return filepath.Join(dir, "vireo", "client-id")
}

// loadOrCreateClientID reuses the stored id, or mints one and stores it 0600.
// A stored id that does not match the protocol's pattern is replaced, so the
// file can be deleted (or hand-edited) to start as somebody else.
func loadOrCreateClientID() (id, path string, err error) {
	path = clientIDPath()
	if b, rerr := os.ReadFile(path); rerr == nil {
		if stored := strings.TrimSpace(string(b)); protocol.ValidateClientID(stored) == nil {
			return stored, path, nil
		}
	}
	id, err = protocol.NewClientID()
	if err != nil {
		return "", path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", path, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", path, fmt.Errorf("write %s: %w", path, err)
	}
	return id, path, nil
}

// checkClientID uses the same rule the daemon enforces, so a bad -id fails
// here with a clear message instead of on the wire.
func checkClientID(id string) error {
	if err := protocol.ValidateClientID(id); err != nil {
		return fmt.Errorf("%v — drop -id to generate one", err)
	}
	return nil
}

// --- screens ---------------------------------------------------------------

func doConnect(opts client.Options, host string) (*conn, error) {
	addr, via, err := client.DiscoverDetail(host, nil)
	if err != nil {
		return nil, err
	}
	opts.Addr = addr
	c, err := client.Connect(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	fmt.Printf("connected to %s (%s)\n", addr, via)
	nc := &conn{Client: c, addr: addr, sessions: map[string]string{}}
	showEndpoint(nc)
	return nc, nil
}

// showEndpoint is the main menu: agents first, then every conversation you
// already have, so a previous thread is one number away.
func showEndpoint(c *conn) {
	c.choices = nil
	agents := c.Agents()

	fmt.Println()
	if len(agents) == 0 {
		fmt.Println("no agents")
	} else {
		fmt.Println("agents:")
		for _, a := range agents {
			c.choices = append(c.choices, choice{kind: "agent", agent: a.Name})
			kind := "one message"
			if a.Sessions {
				kind = "conversation"
			}
			fmt.Printf("  %d) %-16s %s [%s]\n", len(c.choices), a.Name, a.Description, kind)
		}
	}

	type row struct {
		agent   string
		session client.SessionInfo
	}
	var rows []row
	ctx := context.Background()
	for _, a := range agents {
		if !a.Sessions {
			continue
		}
		list, err := c.ListSessions(ctx, a.Name, 10)
		if err != nil {
			continue
		}
		for _, s := range list {
			rows = append(rows, row{agent: a.Name, session: s})
		}
	}
	if len(rows) > 0 {
		fmt.Println()
		fmt.Println("conversations:")
		for _, r := range rows {
			c.choices = append(c.choices, choice{kind: "session", agent: r.agent, session: r.session.ID})
			fmt.Printf("  %d) %-14s %s  %s\n", len(c.choices), r.agent, shortID(r.session.ID), describe(r.session))
		}
	}

	if refs := c.Manifest().Refs; len(refs) > 0 {
		fmt.Println()
		fmt.Println("references:")
		for _, k := range sortedKeys(refs) {
			fmt.Printf("  %-10s %s\n", k, refs[k])
		}
	}

	fmt.Println()
	if len(rows) > 0 {
		fmt.Println("type a number to open an agent or resume a conversation — help for commands")
	} else {
		fmt.Println("type a number or an agent name — help for commands")
	}
}

// showAgent is one agent's screen: its description and its conversations.
func showAgent(c *conn) {
	entry, _ := c.Agent(c.agent)
	fmt.Println()
	fmt.Printf("%s — %s\n", entry.Name, entry.Description)
	if !entry.Sessions {
		fmt.Println("this agent answers single messages; no conversation needed")
		fmt.Println()
		fmt.Println("type anything to send it")
		return
	}

	list, err := c.ListSessions(context.Background(), c.agent, 20)
	if err != nil {
		fmt.Printf("conversations: %v\n", err)
		return
	}
	c.choices = nil
	current := c.sessions[c.agent]
	fmt.Println()
	if len(list) == 0 {
		fmt.Println("no conversations yet — type: new")
		return
	}
	fmt.Println("conversations:")
	for _, s := range list {
		c.choices = append(c.choices, choice{kind: "session", agent: c.agent, session: s.ID})
		marker := " "
		if s.ID == current {
			marker = "*"
		}
		fmt.Printf(" %s%d) %s  %s\n", marker, len(c.choices), shortID(s.ID), describe(s))
	}
	fmt.Println()
	fmt.Println("type a number to continue one · new · back")
}

func openAgent(c *conn, name string) {
	c.agent = name
	showAgent(c)
}

func selectNumber(c *conn, n int) {
	if n < 1 || n > len(c.choices) {
		if len(c.choices) == 0 {
			fmt.Println("nothing to select — type: help")
			return
		}
		fmt.Printf("pick a number between 1 and %d\n", len(c.choices))
		return
	}
	ch := c.choices[n-1]
	switch ch.kind {
	case "agent":
		openAgent(c, ch.agent)
	case "session":
		openAgent(c, ch.agent)
		c.sessions[ch.agent] = ch.session
		fmt.Printf("\nsession %s resumed — type a message\n", shortID(ch.session))
	}
}

// --- sessions --------------------------------------------------------------

func newSession(c *conn, agent string) {
	info, err := c.NewSession(context.Background(), agent, "")
	if err != nil {
		fmt.Printf("new conversation: %v\n", err)
		return
	}
	c.sessions[agent] = info.ID
	fmt.Printf("\nconversation %s started — type a message\n", shortID(info.ID))
}

func resumeSession(c *conn) {
	fmt.Print("number from the list: ")
	var raw string
	fmt.Scanln(&raw)
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		fmt.Println("pick a number from the list")
		return
	}
	selectNumber(c, n)
}

func latestSession(c *conn) {
	list, err := c.ListSessions(context.Background(), c.agent, 1)
	if err != nil || len(list) == 0 {
		fmt.Println("no conversations to resume")
		return
	}
	c.sessions[c.agent] = list[0].ID
	fmt.Printf("\nconversation %s resumed\n", shortID(list[0].ID))
}

func closeSession(c *conn) {
	id := c.sessions[c.agent]
	if id == "" {
		fmt.Println("no conversation open — pick one, or type: new")
		return
	}
	if _, err := c.CloseSession(context.Background(), c.agent, id, "closed from the cli"); err != nil {
		fmt.Printf("close: %v\n", err)
		return
	}
	delete(c.sessions, c.agent)
	fmt.Printf("\nconversation %s closed\n", shortID(id))
}

func sendMessage(c *conn, text string) {
	id := c.sessions[c.agent]
	reply, err := c.SendMessage(context.Background(), c.agent, id, text)
	if err != nil {
		fmt.Printf("\nerror: %v\n", err)
		return
	}
	fmt.Printf("\n%s\n", reply.Text)
	if reply.Session.Summary != "" {
		fmt.Printf("   [%s · turn %d] %q\n", reply.Session.Status, reply.Session.Turns, reply.Session.Summary)
	}
	if reply.Session.Status == "closed" {
		delete(c.sessions, c.agent)
		fmt.Println("   (conversation closed)")
	}
}

// --- formatting ------------------------------------------------------------

func describe(s client.SessionInfo) string {
	when := ""
	if !s.Updated.IsZero() {
		when = s.Updated.Format("Jan 2 15:04")
	}
	summary := s.Summary
	if summary == "" {
		summary = "(no summary)"
	}
	return fmt.Sprintf("%-6s %s  %q", s.Status, when, summary)
}

func prompt(c *conn) string {
	if c.Client == nil {
		return "> "
	}
	if c.agent == "" {
		return "vireo> "
	}
	if id := c.sessions[c.agent]; id != "" {
		return fmt.Sprintf("%s[%s]> ", c.agent, shortID(id))
	}
	return fmt.Sprintf("%s> ", c.agent)
}

func printHelp() {
	fmt.Println("numbers           open an agent, or resume a conversation")
	fmt.Println("agents            back to the main menu (agents + conversations)")
	fmt.Println("<agent>           open an agent by name")
	fmt.Println("new               start a conversation with the open agent")
	fmt.Println("close             close the open conversation")
	fmt.Println("latest            resume the most recent conversation")
	fmt.Println("sessions          list the open agent's conversations")
	fmt.Println("back              back to the main menu")
	fmt.Println("connect <host>    connect to a daemon")
	fmt.Println("anything else     sent as a message to the open agent")
	fmt.Println("help / quit")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
