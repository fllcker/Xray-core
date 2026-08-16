package api

import (
	"strings"

	fllckerService "github.com/xtls/xray-core/app/fllcker/command"
	"github.com/xtls/xray-core/main/commands/base"
)

// cmdFllcker groups this fork's session control commands. They exist so that
// the API can be poked by hand without grpcurl and a copy of the .proto.
var cmdFllcker = &base.Command{
	UsageLine: "{{.Exec}} api fllcker",
	Short:     "Inspect and drop user sessions",
	Long: `
{{.Exec}} {{.LongName}} inspects live sessions and drops them.

Every operation names a user together with an address, never an address on its
own: hundreds of users can share one carrier-grade NAT address.
`,
	Commands: []*base.Command{
		cmdFllckerSessions,
		cmdFllckerKick,
		cmdFllckerBans,
		cmdFllckerUnban,
	},
}

// addressList collects a repeatable -ip flag, and also splits commas inside one
// value, so both spellings work:
//
//	-ip 1.1.1.1 -ip 2.2.2.2
//	-ip 1.1.1.1,2.2.2.2
type addressList []string

func (l *addressList) String() string { return strings.Join(*l, ",") }

func (l *addressList) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

var cmdFllckerSessions = &base.Command{
	CustomFlags: true,
	UsageLine:   "{{.Exec}} api fllcker sessions [--server=127.0.0.1:8080] [-email '']",
	Short:       "List live sessions",
	Long: `
List the addresses each user currently holds.

Arguments:

	-s, -server <server:port>
		The API server address. Default 127.0.0.1:8080

	-t, -timeout <seconds>
		Timeout in seconds for calling API. Default 3

	-email
		Limit to one user. Omit for every user.

Each address reports two timestamps with different jobs. "since" is when the
address first appeared, so it answers who arrived when. "lastSeen" advances on
every new stream, so it answers whether the address is still doing anything: one
left behind by a network switch stops opening streams, while a device in use
opens them continuously.

Example:

	{{.Exec}} {{.LongName}} --server=127.0.0.1:8080
	{{.Exec}} {{.LongName}} -email "user@example.com"
`,
	Run: executeFllckerSessions,
}

func executeFllckerSessions(cmd *base.Command, args []string) {
	setSharedFlags(cmd)
	email := cmd.Flag.String("email", "", "")
	cmd.Flag.Parse(args)

	conn, ctx, close := dialAPIServer()
	defer close()

	client := fllckerService.NewFllckerServiceClient(conn)
	resp, err := client.ListSessions(ctx, &fllckerService.ListSessionsRequest{Email: *email})
	if err != nil {
		base.Fatalf("failed to list sessions: %s", err)
	}
	showJSONResponse(resp)
}

var cmdFllckerKick = &base.Command{
	CustomFlags: true,
	UsageLine:   "{{.Exec}} api fllcker kick [--server=127.0.0.1:8080] -email '' [-ip ''] [-seconds 0]",
	Short:       "Drop a user's sessions and block reconnection",
	Long: `
Close a user's connections at the given addresses and refuse them there for a
while.

Arguments:

	-s, -server <server:port>
		The API server address. Default 127.0.0.1:8080

	-t, -timeout <seconds>
		Timeout in seconds for calling API. Default 3

	-email
		The user. Required.

	-ip
		Address to drop. Repeatable, and accepts a comma separated list.
		Omit to drop every address the user holds.

	-seconds
		How long to refuse the user at these addresses. Default 0, which
		closes connections without blocking, and on its own achieves
		little: a client reconnects in about the time it takes to notice.

Pass every address in one call rather than looping. Kicking one at a time leaves
a window in which the client dropped by an earlier call is already back on an
address that was not named yet.

Example:

	{{.Exec}} {{.LongName}} -email "user@example.com" -ip 1.1.1.1,2.2.2.2 -seconds 10
	{{.Exec}} {{.LongName}} -email "user@example.com" -seconds 5
`,
	Run: executeFllckerKick,
}

func executeFllckerKick(cmd *base.Command, args []string) {
	setSharedFlags(cmd)
	email := cmd.Flag.String("email", "", "")
	seconds := cmd.Flag.Uint("seconds", 0, "")
	var ips addressList
	cmd.Flag.Var(&ips, "ip", "")
	cmd.Flag.Parse(args)

	// Checked here as well as on the server so the mistake reads as a usage
	// error instead of a gRPC status.
	if *email == "" {
		base.Fatalf("-email is required")
	}

	conn, ctx, close := dialAPIServer()
	defer close()

	client := fllckerService.NewFllckerServiceClient(conn)
	resp, err := client.Kick(ctx, &fllckerService.KickRequest{
		Email:      *email,
		Ips:        ips,
		BanSeconds: uint32(*seconds),
	})
	if err != nil {
		base.Fatalf("failed to kick: %s", err)
	}
	showJSONResponse(resp)
}

var cmdFllckerBans = &base.Command{
	CustomFlags: true,
	UsageLine:   "{{.Exec}} api fllcker bans [--server=127.0.0.1:8080] [-email '']",
	Short:       "List blocks in force",
	Long: `
List the blocks currently in force. Expired ones are never shown.

Arguments:

	-s, -server <server:port>
		The API server address. Default 127.0.0.1:8080

	-t, -timeout <seconds>
		Timeout in seconds for calling API. Default 3

	-email
		Limit to one user. Omit for every user.

Example:

	{{.Exec}} {{.LongName}}
	{{.Exec}} {{.LongName}} -email "user@example.com"
`,
	Run: executeFllckerBans,
}

func executeFllckerBans(cmd *base.Command, args []string) {
	setSharedFlags(cmd)
	email := cmd.Flag.String("email", "", "")
	cmd.Flag.Parse(args)

	conn, ctx, close := dialAPIServer()
	defer close()

	client := fllckerService.NewFllckerServiceClient(conn)
	resp, err := client.ListBans(ctx, &fllckerService.ListBansRequest{Email: *email})
	if err != nil {
		base.Fatalf("failed to list bans: %s", err)
	}
	showJSONResponse(resp)
}

var cmdFllckerUnban = &base.Command{
	CustomFlags: true,
	UsageLine:   "{{.Exec}} api fllcker unban [--server=127.0.0.1:8080] -email '' [-ip '']",
	Short:       "Lift a block early",
	Long: `
Lift a block before it expires.

Arguments:

	-s, -server <server:port>
		The API server address. Default 127.0.0.1:8080

	-t, -timeout <seconds>
		Timeout in seconds for calling API. Default 3

	-email
		The user. Required.

	-ip
		Address to unblock. Repeatable, and accepts a comma separated
		list. Omit to lift every block on the user.

Example:

	{{.Exec}} {{.LongName}} -email "user@example.com" -ip 1.1.1.1
	{{.Exec}} {{.LongName}} -email "user@example.com"
`,
	Run: executeFllckerUnban,
}

func executeFllckerUnban(cmd *base.Command, args []string) {
	setSharedFlags(cmd)
	email := cmd.Flag.String("email", "", "")
	var ips addressList
	cmd.Flag.Var(&ips, "ip", "")
	cmd.Flag.Parse(args)

	if *email == "" {
		base.Fatalf("-email is required")
	}

	conn, ctx, close := dialAPIServer()
	defer close()

	client := fllckerService.NewFllckerServiceClient(conn)
	resp, err := client.Unban(ctx, &fllckerService.UnbanRequest{
		Email: *email,
		Ips:   ips,
	})
	if err != nil {
		base.Fatalf("failed to unban: %s", err)
	}
	showJSONResponse(resp)
}
