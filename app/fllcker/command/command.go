package command

import (
	"context"
	"time"

	"github.com/xtls/xray-core/app/fllcker"
	"github.com/xtls/xray-core/common"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// fllckerServer is an implementation of FllckerService.
//
// It holds no state: the session and ban registries live in the fllcker package
// itself, so there is nothing to inject and nothing to keep in sync.
type fllckerServer struct{}

func NewFllckerServer() FllckerServiceServer {
	return &fllckerServer{}
}

// unixSeconds converts an internal nanosecond deadline into the unix seconds
// the API reports.
//
// Rounded up on purpose: a caller that sleeps until this moment and retries
// must not arrive while the block is still live. Reporting a deadline slightly
// late costs nothing, reporting it early causes a confusing rejection.
func unixSeconds(nano int64) int64 {
	if nano <= 0 {
		return 0
	}
	return (nano + int64(time.Second) - 1) / int64(time.Second)
}

func (s *fllckerServer) Kick(ctx context.Context, request *KickRequest) (*KickResponse, error) {
	// Rejected rather than treated as "everyone": an operation without a user
	// would be scoped to addresses alone, and one address can carry hundreds of
	// unrelated users behind a carrier-grade NAT.
	if request.Email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}

	closed, bannedUntil := fllcker.Kick(
		request.Email,
		request.Ips,
		time.Duration(request.BanSeconds)*time.Second,
	)

	return &KickResponse{
		Closed:      int32(closed),
		BannedUntil: unixSeconds(bannedUntil),
	}, nil
}

func (s *fllckerServer) ListSessions(ctx context.Context, request *ListSessionsRequest) (*ListSessionsResponse, error) {
	users := fllcker.Sessions(request.Email)

	response := &ListSessionsResponse{Users: make([]*UserSessions, 0, len(users))}
	for _, user := range users {
		entry := &UserSessions{
			Email: user.Email,
			Ips:   make([]*SessionIP, 0, len(user.IPs)),
		}
		for _, ip := range user.IPs {
			entry.Ips = append(entry.Ips, &SessionIP{
				Ip:          ip.IP,
				Conns:       int32(ip.Conns),
				Since:       ip.Since,
				LastSeen:    ip.LastSeen,
				BannedUntil: unixSeconds(fllcker.BannedUntil(user.Email, ip.IP)),
			})
		}
		response.Users = append(response.Users, entry)
	}
	return response, nil
}

func (s *fllckerServer) ListBans(ctx context.Context, request *ListBansRequest) (*ListBansResponse, error) {
	live := fllcker.Bans(request.Email)

	response := &ListBansResponse{Bans: make([]*BanEntry, 0, len(live))}
	for _, ban := range live {
		response.Bans = append(response.Bans, &BanEntry{
			Email: ban.Email,
			Ip:    ban.IP,
			Until: unixSeconds(ban.Until),
		})
	}
	return response, nil
}

func (s *fllckerServer) Unban(ctx context.Context, request *UnbanRequest) (*UnbanResponse, error) {
	if request.Email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	return &UnbanResponse{Lifted: fllcker.Unban(request.Email, request.Ips)}, nil
}

func (s *fllckerServer) mustEmbedUnimplementedFllckerServiceServer() {}

type service struct{}

func (s *service) Register(server *grpc.Server) {
	RegisterFllckerServiceServer(server, NewFllckerServer())
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, cfg interface{}) (interface{}, error) {
		return new(service), nil
	}))
}
