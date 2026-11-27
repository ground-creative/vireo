// Command vireo-chat is a chat client for vireo daemons.
//
//	vireo-chat                 # identity is generated once and reused
//	vireo-chat localhost       # connect on start
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

// conn is the shell's connection plus its mode state.
type conn struct {
	*client.Client
	addr     string
	agent    string                          // open agent; "" = endpoint level
	sessions map[string]string               // agent → current session id
	listed   map[string][]client.SessionInfo // agent → conversations last printed
	numbered []string                        // agent names, in the order printed
}

func main() {
	proto := flag.String("proto", "tls", "transport: tls or quic")
	insecure := flag.Bool("insecure", false, "skip certificate verification (UNSAFE; local development only)")
	caFile := flag.String("ca", "", "PEM bundle to trust for this daemon, e.g. certs/cert.pem (verification stays on)")
	idFlag := flag.String("id", "", "client id override (default: generated once and stored in the config dir)")
	name := flag.String("name", "vireo-chat", "human-readable client name")
	timeout := flag.Duration("timeout", 120*time.Second, "per-request timeout; a model-backed agent can take a minute or more")
	flag.Parse()

	// Verification is on by default: with it off the traffic is still encrypted,
	// but nothing binds the connection to the business being contacted, so an
	// active attacker on the path can impersonate the endpoint (SEC-6).
	if *insecure {
		fmt.Println("warning: -insecure disables certificate verification; the endpoint's identity is not established")
	}
	if *caFile != "" && *insecure {
		fmt.Println("note: -ca has no effect while -insecure is set; verification is skipped entirely")
	}

	// The host may be given directly ("vireo-chat localhost:7777") or after
	// the word "connect" ("vireo-chat connect localhost"), matching the
	// command of the same name inside the shell.
	host := ""
	if args := flag.Args(); len(args) > 0 {
		host = strings.TrimSpace(args[0])
		if host == "connect" || host == "c" {
			host = ""
			if len(args) > 1 {
				host = strings.TrimSpace(args[1])
			} else {
				fmt.Println("usage: vireo-chat connect <host>")
				return
			}
		}
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
		// Progress lines arrive while the agent is still working; print them as
		// they come so a slow turn looks alive instead of frozen.
		OnPartial: func(p protocol.PartialPayload) {
			fmt.Printf("\n  · %s", p.Text)
		},
		Insecure: *insecure,
		CAFile:   *caFile,
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
		fmt.Printf("usage: vireo-chat <host>   — or type: connect <host>   (e.g. connect localhost)\n")
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
			selectAgent(c, n)
			continue
		}

		switch cmd {
		case "agents", "menu":
			showEndpoint(c)
			continue
		case "back":
			showEndpoint(c)
			continue
		case "sessions", "chats", "list", "conversations":
			if c.agent == "" {
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
				fmt.Println("open an agent first, or type a conversation id on the main screen")
				continue
			}
			resumeSession(c, strings.TrimSpace(strings.TrimPrefix(line, fields[0])))
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

		// A conversation id resumes it, wherever it was last printed.
		if resumeByID(c, line) {
			continue
		}

		if c.agent == "" {
			fmt.Println("type an agent number, an agent name, a conversation id, or: help")
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
	addr, serverName, via, err := client.DiscoverTarget(host, nil)
	if err != nil {
		return nil, err
	}
	opts.Addr = addr
	// The certificate must cover the name the operator typed, not wherever the
	// SRV record pointed (PROTO-47, SEC-9).
	opts.ServerName = serverName
	c, err := client.Connect(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	fmt.Printf("connected to %s (%s)\n", addr, via)
	nc := &conn{Client: c, addr: addr, sessions: map[string]string{}}
	showEndpoint(nc)
	return nc, nil
}

// showEndpoint is the main menu: agents are numbered, conversations keep their
// stable ids. An id is what you type to continue one — it does not shift when
// another conversation appears.
func showEndpoint(c *conn) {
	c.agent = ""
	c.numbered = nil
	c.listed = map[string][]client.SessionInfo{}
	agents := c.Agents()

	fmt.Println()
	if len(agents) == 0 {
		fmt.Println("no agents")
	} else {
		fmt.Println("agents:")
		for _, a := range agents {
			c.numbered = append(c.numbered, a.Name)
			kind := "one message"
			if a.Sessions {
				kind = "conversation"
			}
			fmt.Printf("  %d) %-16s %s [%s]\n", len(c.numbered), a.Name, a.Description, kind)
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
		c.listed[a.Name] = list
		for _, s := range list {
			rows = append(rows, row{agent: a.Name, session: s})
		}
	}
	if len(rows) > 0 {
		fmt.Println()
		fmt.Println("recent conversations:")
		for _, r := range rows {
			fmt.Printf("  %s  %-14s %s\n", shortID(r.session.ID), r.agent, describe(r.session))
		}
	}

	if refs := c.Manifest().Refs; len(refs) > 0 {
		fmt.Println()
		fmt.Println("references:")
		for _, k := range sortedKeys(refs) {
			fmt.Printf("  %-10s %s\n", k, refs[k])
		}
	}

	// links is the 1.1 shape and says what each reference is; refs is the
	// deprecated map and is shown only when a daemon still sends it alone.
	if links := c.Manifest().Links; len(links) > 0 && len(c.Manifest().Refs) == 0 {
		fmt.Println()
		fmt.Println("references:")
		for _, l := range links {
			label := l.Label
			if label == "" {
				label = l.Rel
			}
			fmt.Printf("  %-10s %s\n", label, l.Href)
		}
	}

	fmt.Println()
	if len(rows) > 0 {
		fmt.Println("type an agent number or name, or a conversation id to resume — help")
	} else {
		fmt.Println("type an agent number or name — help")
	}
}

// showAgent is one agent's screen: its description and its conversations.
// Numbers are not used here; conversations are addressed by id.
func showAgent(c *conn) {
	entry, _ := c.Agent(c.agent)
	c.numbered = nil
	fmt.Println()
	fmt.Printf("%s — %s\n", entry.Name, entry.Description)
	if len(entry.Tags) > 0 {
		fmt.Printf("tags: %s\n", strings.Join(entry.Tags, ", "))
	}
	if len(entry.Links) > 0 {
		fmt.Println("links:")
		for _, l := range entry.Links {
			label := l.Label
			if label == "" {
				label = l.Rel
			}
			fmt.Printf("  %-10s %s\n", label, l.Href)
		}
	}
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
	c.listed[c.agent] = list
	current := c.sessions[c.agent]
	fmt.Println()
	if len(list) == 0 {
		fmt.Println("no conversations yet — type: new")
		return
	}
	fmt.Println("conversations:")
	for _, s := range list {
		marker := " "
		if s.ID == current {
			marker = "*"
		}
		fmt.Printf(" %s%s  %s\n", marker, shortID(s.ID), describe(s))
	}
	fmt.Println()
	fmt.Println("type a conversation id to continue one · new · back")
}

func openAgent(c *conn, name string) {
	c.agent = name
	showAgent(c)
}

// selectAgent opens the agent at position n on the last main menu.
func selectAgent(c *conn, n int) {
	if len(c.numbered) == 0 {
		fmt.Println("numbers apply to the agent list — type: agents")
		return
	}
	if n < 1 || n > len(c.numbered) {
		fmt.Printf("pick an agent number between 1 and %d\n", len(c.numbered))
		return
	}
	openAgent(c, c.numbered[n-1])
}

// resumeByID continues the conversation whose id — or unambiguous id prefix —
// matches, using whatever was printed last. It reports whether it handled the
// input, so an unrecognised line can still be sent as a message.
func resumeByID(c *conn, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	type hit struct {
		agent string
		s     client.SessionInfo
	}
	var hits []hit
	for agent, list := range c.listed {
		for _, s := range list {
			if s.ID == token || strings.HasPrefix(s.ID, token) {
				hits = append(hits, hit{agent: agent, s: s})
			}
		}
	}
	switch len(hits) {
	case 0:
		return false
	case 1:
		h := hits[0]
		openAgent(c, h.agent)
		c.sessions[h.agent] = h.s.ID
		fmt.Printf("\nconversation %s resumed — type a message\n", shortID(h.s.ID))
		return true
	default:
		fmt.Println("that id is ambiguous:")
		for _, h := range hits {
			fmt.Printf("  %s  %s\n", shortID(h.s.ID), h.agent)
		}
		return true
	}
}

// --- sessions --------------------------------------------------------------

func newSession(c *conn, agent string) {
	started, err := c.NewSession(context.Background(), agent, "")
	if err != nil {
		fmt.Printf("new conversation: %v\n", err)
		return
	}
	c.sessions[agent] = started.Session.ID
	fmt.Printf("\nconversation %s started — type a message\n", shortID(started.Session.ID))
	if started.StartMessage != "" {
		fmt.Printf("\n%s\n", started.StartMessage)
	}
}

// resumeSession continues a conversation named by id. With no id it lists the
// ones the screen last showed, so the caller can pick one by copying its id.
func resumeSession(c *conn, id string) {
	if id == "" {
		list := c.listed[c.agent]
		if len(list) == 0 {
			fmt.Println("no conversations listed — type: sessions")
			return
		}
		fmt.Println("copy an id:")
		for _, s := range list {
			fmt.Printf("  %s  %s\n", shortID(s.ID), describe(s))
		}
		fmt.Println("then type: resume <id>")
		return
	}
	if !resumeByID(c, id) {
		fmt.Printf("no conversation %q in the list — type: sessions\n", id)
	}
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
