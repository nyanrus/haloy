package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/haloydev/haloy/internal/apitypes"
	"github.com/haloydev/haloy/internal/config"
	"github.com/haloydev/haloy/internal/docker"
	"github.com/haloydev/haloy/internal/logging"
	"github.com/haloydev/haloy/internal/proxywire"
	"github.com/haloydev/haloy/internal/storage"
	"golang.org/x/time/rate"
)

type APIServer struct {
	router                    *http.ServeMux
	db                        *storage.DB
	logBroker                 logging.StreamPublisher
	logLevel                  slog.Level
	logger                    *slog.Logger
	apiToken                  string
	rateLimiter               *RateLimiter
	layerRateLimiter          *RateLimiter
	tunnelPolicy              TunnelPolicy
	tunnelCapacity            *tunnelCapacity
	tunnelTracker             *connectionTracker
	tunnelResolver            tunnelTargetResolver
	tunnelDialer              tunnelDialer
	serverMu                  sync.Mutex
	httpServer                *http.Server
	uploadDiskSpaceCheck      func(context.Context, int64) error
	layerUploadDiskSpaceCheck func(context.Context, int64) error
	assembleDiskSpaceCheck    func(context.Context, apitypes.ImageAssembleRequest) error
	imageDiskSpaceCheck       func(context.Context, apitypes.ImageDiskSpaceCheckRequest) (diskSpaceCheckResult, error)
	imagePrune                func(context.Context, apitypes.ImagePruneRequest) (apitypes.ImagePruneResponse, error)
	registryAuthProvider      func(config.Image) (*config.RegistryAuth, error)
	registryLoginCheck        func(context.Context, config.RegistryAuth) error
	proxyStatus               func(context.Context) (*proxywire.Status, error)
}

// SetProxyStatusFunc wires the haloy-proxy status lookup used by the version
// endpoint. It is optional; when unset or failing, proxy fields are omitted.
func (s *APIServer) SetProxyStatusFunc(fn func(context.Context) (*proxywire.Status, error)) {
	s.proxyStatus = fn
}

func NewServer(apiToken string, db *storage.DB, logBroker logging.StreamPublisher, logLevel slog.Level, policies ...TunnelPolicy) *APIServer {
	policy := DefaultTunnelPolicy()
	if len(policies) > 0 {
		policy = policies[0].withDefaults()
	}
	s := &APIServer{
		router:           http.NewServeMux(),
		db:               db,
		logBroker:        logBroker,
		logLevel:         logLevel,
		logger:           logging.NewLogger(logLevel, logBroker),
		apiToken:         apiToken,
		rateLimiter:      NewRateLimiter(rate.Limit(5), 10),   // 5 req/sec, burst of 10
		layerRateLimiter: NewRateLimiter(rate.Limit(50), 100), // 50 req/sec, burst of 100 for layer uploads
		tunnelPolicy:     policy,
		tunnelCapacity:   newTunnelCapacity(policy),
		tunnelTracker:    newConnectionTracker(),
	}
	s.tunnelResolver = s.resolveTunnelTarget
	s.tunnelDialer = defaultTunnelDialer
	s.registryAuthProvider = loadServerRegistryAuthForImage
	s.registryLoginCheck = docker.VerifyRegistryLogin
	s.setupRoutes()
	return s
}

func loadServerRegistryAuthForImage(image config.Image) (*config.RegistryAuth, error) {
	registries, err := loadServerRegistries()
	if err != nil {
		return nil, err
	}
	return registries.AuthForImage(image)
}

func shouldApplyServerRegistryAuth(image *config.Image) bool {
	if image == nil || image.RegistryAuth != nil {
		return false
	}
	if image.ShouldBuild() && image.GetEffectivePushStrategy() == config.BuildPushOptionServer {
		return false
	}
	return true
}

func (s *APIServer) applyServerRegistryAuth(targetConfig *config.TargetConfig) error {
	if targetConfig == nil || !shouldApplyServerRegistryAuth(targetConfig.Image) {
		return nil
	}
	if s.registryAuthProvider == nil {
		return nil
	}

	auth, err := s.registryAuthProvider(*targetConfig.Image)
	if err != nil {
		return err
	}
	if auth == nil {
		return nil
	}

	targetConfig.Image.RegistryAuth = auth
	return nil
}

func (s *APIServer) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 5 * time.Second,  // Prevent Slowloris
		IdleTimeout:       60 * time.Second, // Keep-alive connections
	}

	s.serverMu.Lock()
	s.httpServer = srv
	s.serverMu.Unlock()

	return srv.ListenAndServe()
}

// Shutdown stops accepting API requests and drains active hijacked tunnels.
// Tunnels still open when ctx expires are force-closed.
func (s *APIServer) Shutdown(ctx context.Context) error {
	var errs []error

	s.serverMu.Lock()
	srv := s.httpServer
	s.serverMu.Unlock()
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if s.tunnelTracker != nil {
		if err := s.tunnelTracker.shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
