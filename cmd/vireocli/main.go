// Command vireocli is a command-line peer for vireo daemons.
//
//	go run ./cmd/vireocli -id me localhost
//
// It is a protocol client, not a chatbot: it speaks the wire protocol and
// prints what the daemon answers. The AI lives behind the MCP bridge, which
// runs where the AI runs.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"vireo/internal/client"
)

// banner is printed once at startup so the tool says what it is.
const banner = `vireocli — raw protocol client for a vireo daemon.

This is not an AI and it does not talk back: it sends what you type to the
daemon and prints what the daemon answers. The backend behind the daemon may
be a demo, in which case its replies are canned. For a real assistant, connect
an AI host through the MCP bridge instead.`

type conn struct {
	*client.Client
	addr     string
	agent    string            // open agent; "" = endpoint level
	sessions map[string]string // agent → current session id
	listed   map[string][]client.SessionInfo
}

func main() {
	proto := flag.String("proto", "tls", "transport: tls or quic")
	insecure := flag.Bool("insecure", true, "skip certificate verification (dev only)")
	id := flag.String("id", "", "your unique client id (required by the daemon; minted if empty)")
	name := flag.String("name", "vireocli", "human-readable client name")
	timeout := flag.Duration("timeout", 15*time.Second, "per-request timeout")
	flag.Parse()

	host := ""
	if args := flag.Args(); len(args) > 0 {
		host = strings.TrimSpace(args[0])
	}

	clientID := strings.TrimSpace(*id)
	if clientID == "" {
		clientID = mintHex()
		fmt.Printf("note: no -id given, minted a fresh client id (resume across runs needs a stable -id)\n")
	}
	fmt.Printf("client id: %s\n", clientID)

	opts := client.Options{
		ClientID: clientID,
		Name:     *name,
		Timeout:  *timeout,
		Insecure: *insecure,
		UseQUIC:  *proto == "quic",
	}

	c := &conn{sessions: map[string]string{}, listed: map[string][]client.SessionInfo{}}
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

	fmt.Println()
	fmt.Println(banner)
	fmt.Println()

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

		// Global commands, any level.
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

		// Agent-level commands.
		switch cmd {
		case "agents":
			showAgents(c)
			continue
		case "back":
			if c.agent != "" {
				c.agent = ""
			}
			continue
		case "sessions", "chats":
			if c.agent == "" {
				fmt.Println("pick an agent first")
				continue
			}
			showSessions(c)
			continue
		case "new":
			if c.agent == "" {
				fmt.Println("pick an agent first")
				continue
			}
			newSession(c)
			continue
		case "resume":
			if c.agent == "" {
				fmt.Println("pick an agent first")
				continue
			}
			resumeSession(c)
			continue
		case "latest":
			if c.agent == "" {
				fmt.Println("pick an agent first")
				continue
			}
			latestSession(c)
			continue
		case "close":
			if c.agent == "" {
				fmt.Println("pick an agent first")
				continue
			}
			closeSession(c)
			continue
		}

		// Agent names open the agent.
		if _, ok := c.Agent(cmd); ok {
			openAgent(c, cmd)
			continue
		}

		// Anything else is a message to the open agent.
		if c.agent == "" {
			fmt.Println("pick an agent first — type: agents")
			continue
		}
		sendMessage(c, line)
	}
}

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
	nc := &conn{
		Client:   c,
		addr:     addr,
		sessions: map[string]string{},
		listed:   map[string][]client.SessionInfo{},
	}
	showAgents(nc)
	return nc, nil
}

func showAgents(c *conn) {
	agents := c.Agents()
	if len(agents) == 0 {
		fmt.Println("no agents")
		return
	}
	fmt.Println()
	fmt.Println("agents:")
	for i, a := range agents {
		kind := "one message"
		if a.Sessions {
			kind = "conversation"
		}
		fmt.Printf("  %d) %-16s %s [%s]\n", i+1, a.Name, a.Description, kind)
	}
	if len(c.Manifest().Refs) > 0 {
		fmt.Println()
		fmt.Println("references:")
		for _, k := range sortedKeys(c.Manifest().Refs) {
			fmt.Printf("  %-10s %s\n", k, c.Manifest().Refs[k])
		}
	}
	fmt.Println()
	fmt.Println("type a number or an agent name — help for commands")
}

func openAgent(c *conn, name string) {
	c.agent = name
	a, _ := c.Agent(name)
	fmt.Println()
	fmt.Printf("%s — %s\n", a.Name, a.Description)
	if !a.Sessions {
		fmt.Println("this agent answers single messages; no session needed")
	}
	showSessions(c)
}

func showSessions(c *conn) {
	ctx := context.Background()
	list, err := c.ListSessions(ctx, c.agent, 20)
	if err != nil {
		fmt.Printf("sessions: %v\n", err)
		return
	}
	c.listed[c.agent] = list
	fmt.Println()
	if len(list) == 0 {
		fmt.Println("no sessions yet — type: new")
		return
	}
	fmt.Println("sessions:")
	for i, s := range list {
		status := s.Status
		if s.Updated.IsZero() {
			status = s.Status
		} else {
			status = fmt.Sprintf("%s · %s", s.Status, s.Updated.Format("Jan 2 15:04"))
		}
		summary := s.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		fmt.Printf("  %d) %s  %s  %q\n", i+1, s.ID[:8], status, summary)
	}
	fmt.Println()
	fmt.Println("type: new · resume <number> · latest · close")
}

func newSession(c *conn) {
	ctx := context.Background()
	info, err := c.NewSession(ctx, c.agent, "")
	if err != nil {
		fmt.Printf("new session: %v\n", err)
		return
	}
	c.sessions[c.agent] = info.ID
	fmt.Printf("\nsession %s opened\n", info.ID[:8])
}

func resumeSession(c *conn) {
	list := c.listed[c.agent]
	if len(list) == 0 {
		fmt.Println("no sessions to resume")
		return
	}
	fmt.Print("resume which session? ")
	var raw string
	fmt.Scanln(&raw)
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > len(list) {
		fmt.Println("pick a number from the list")
		return
	}
	c.sessions[c.agent] = list[n-1].ID
	fmt.Printf("\nsession %s resumed\n", list[n-1].ID[:8])
}

func latestSession(c *conn) {
	list := c.listed[c.agent]
	if len(list) == 0 {
		fmt.Println("no sessions to resume")
		return
	}
	c.sessions[c.agent] = list[0].ID
	fmt.Printf("\nsession %s resumed\n", list[0].ID[:8])
}

func closeSession(c *conn) {
	id := c.sessions[c.agent]
	if id == "" {
		fmt.Println("no open session")
		return
	}
	if _, err := c.CloseSession(context.Background(), c.agent, id, "closed from the cli"); err != nil {
		fmt.Printf("close: %v\n", err)
		return
	}
	delete(c.sessions, c.agent)
	fmt.Printf("\nsession %s closed\n", id[:8])
}

func sendMessage(c *conn, text string) {
	ctx := context.Background()
	id := c.sessions[c.agent]
	reply, err := c.SendMessage(ctx, c.agent, id, text)
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

func prompt(c *conn) string {
	if c.Client == nil {
		return "> "
	}
	if c.agent == "" {
		return "vireo> "
	}
	if c.sessions[c.agent] == "" {
		return fmt.Sprintf("%s> ", c.agent)
	}
	return fmt.Sprintf("%s[%s]> ", c.agent, c.sessions[c.agent][:8])
}

func printHelp() {
	fmt.Println("commands:")
	fmt.Println("  connect <host>        connect to a daemon (SRV _vireo._tcp, then port 7777)")
	fmt.Println("  agents                list the agents behind the endpoint")
	fmt.Println("  <agent>               open an agent")
	fmt.Println("  new                   start a new session with the open agent")
	fmt.Println("  resume <n>            resume a listed session")
	fmt.Println("  latest                resume the most recent session")
	fmt.Println("  sessions              list your sessions with the open agent")
	fmt.Println("  close                 close the open session")
	fmt.Println("  back                  back to the agent list")
	fmt.Println("  help / quit")
	fmt.Println()
	fmt.Println("anything else is sent as a message to the open agent")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func mintHex() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "vireocli"
	}
	return hex.EncodeToString(b)
}
