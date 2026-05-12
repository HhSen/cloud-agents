package oidc

import (
	"context"
	"errors"
	"sync"

	coreidoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/your-org/platform-backend/pkg/config"
	"golang.org/x/oauth2"
)

var ErrNonceMismatch = errors.New("oidc: nonce mismatch")

type Service struct {
	cfg      config.OIDCConfig
	mu       sync.Mutex
	provider *coreidoidc.Provider
	verifier *coreidoidc.IDTokenVerifier
}

func New(cfg config.OIDCConfig) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) getProvider(ctx context.Context) (*coreidoidc.Provider, *coreidoidc.IDTokenVerifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, s.verifier, nil
	}
	p, err := coreidoidc.NewProvider(ctx, s.cfg.DiscoveryURL)
	if err != nil {
		return nil, nil, err
	}
	s.provider = p
	s.verifier = p.Verifier(&coreidoidc.Config{ClientID: s.cfg.ClientID})
	return s.provider, s.verifier, nil
}

func (s *Service) oauth2Config(ctx context.Context, redirectURI string) (*oauth2.Config, error) {
	p, _, err := s.getProvider(ctx)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		RedirectURL:  redirectURI,
		Endpoint:     p.Endpoint(),
		Scopes:       []string{coreidoidc.ScopeOpenID, "profile", "email"},
	}, nil
}

func (s *Service) AuthURL(ctx context.Context, redirectURI, state, nonce string) (string, error) {
	cfg, err := s.oauth2Config(ctx, redirectURI)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOnline, coreidoidc.Nonce(nonce)), nil
}

func (s *Service) ExchangeCode(ctx context.Context, code, redirectURI string) (*oauth2.Token, error) {
	cfg, err := s.oauth2Config(ctx, redirectURI)
	if err != nil {
		return nil, err
	}
	return cfg.Exchange(ctx, code)
}

func (s *Service) VerifyIDToken(ctx context.Context, rawIDToken, nonce string) (*coreidoidc.IDToken, error) {
	_, verifier, err := s.getProvider(ctx)
	if err != nil {
		return nil, err
	}
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, err
	}
	if idToken.Nonce != nonce {
		return nil, ErrNonceMismatch
	}
	return idToken, nil
}
